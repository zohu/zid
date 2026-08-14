package zidk8s

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zohu/zid"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	clientcoordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
)

// Options controls Kubernetes Lease-backed worker IDs.
type Options struct {
	Namespace        string
	Identity         string
	LeaseNamePrefix  string
	Config           *rest.Config
	TTL              time.Duration
	RenewInterval    time.Duration
	SafetyMargin     time.Duration
	OperationTimeout time.Duration
}

func DefaultOptions() Options {
	return Options{
		Namespace:        os.Getenv("NAMESPACE"),
		Identity:         os.Getenv("POD_UID"),
		LeaseNamePrefix:  "zid-worker-",
		TTL:              30 * time.Second,
		RenewInterval:    10 * time.Second,
		SafetyMargin:     10 * time.Second,
		OperationTimeout: 3 * time.Second,
	}
}

// Manager allocates exclusive worker IDs through coordination.k8s.io Leases.
type Manager struct {
	client    clientcoordinationv1.LeaseInterface
	namespace string
	identity  string
	options   Options
}

func New(options Options) (*Manager, error) {
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	config := normalized.Config
	if config == nil {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("zidk8s: load in-cluster config: %w", err)
		}
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("zidk8s: create Kubernetes client: %w", err)
	}
	return newManager(clientset.CoordinationV1().Leases(normalized.Namespace), normalized)
}

// ConfigureDefault constructs a Kubernetes manager and installs it as zid's
// package-level managed Generator. Call it once during process startup, before
// any zid package-level generation or extraction helper is used.
func ConfigureDefault(ctx context.Context, managerOptions Options, generatorOptions zid.Options) error {
	manager, err := New(managerOptions)
	if err != nil {
		return err
	}
	return zid.ConfigureDefault(ctx, manager, generatorOptions)
}

func newManager(client clientcoordinationv1.LeaseInterface, options Options) (*Manager, error) {
	if isNilInterface(client) {
		return nil, errors.New("zidk8s: LeaseInterface must not be nil")
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	return &Manager{
		client:    client,
		namespace: normalized.Namespace,
		identity:  normalized.Identity,
		options:   normalized,
	}, nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func normalizeOptions(options Options) (Options, error) {
	defaults := DefaultOptions()
	if options.Namespace == "" {
		options.Namespace = defaults.Namespace
	}
	if options.Identity == "" {
		options.Identity = defaults.Identity
	}
	if options.LeaseNamePrefix == "" {
		options.LeaseNamePrefix = defaults.LeaseNamePrefix
	}
	if options.TTL == 0 {
		options.TTL = defaults.TTL
	}
	if options.RenewInterval == 0 {
		options.RenewInterval = defaults.RenewInterval
	}
	if options.SafetyMargin == 0 {
		options.SafetyMargin = defaults.SafetyMargin
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaults.OperationTimeout
	}
	options.LeaseNamePrefix = strings.TrimSuffix(options.LeaseNamePrefix, "-") + "-"

	if options.Namespace == "" {
		return Options{}, errors.New("zidk8s: Namespace must not be empty")
	}
	if options.Identity == "" {
		return Options{}, errors.New("zidk8s: Identity or POD_UID must not be empty")
	}
	if options.LeaseNamePrefix == "-" {
		return Options{}, errors.New("zidk8s: LeaseNamePrefix must not be empty")
	}
	if options.TTL <= 0 || options.SafetyMargin <= 0 || options.SafetyMargin >= options.TTL {
		return Options{}, errors.New("zidk8s: require 0 < SafetyMargin < TTL")
	}
	if options.TTL > time.Duration(math.MaxInt32)*time.Second {
		return Options{}, errors.New("zidk8s: TTL exceeds Kubernetes LeaseDurationSeconds")
	}
	if options.RenewInterval <= 0 || options.RenewInterval >= options.TTL-options.SafetyMargin {
		return Options{}, errors.New("zidk8s: RenewInterval must be shorter than the usable lease duration")
	}
	if options.OperationTimeout <= 0 || options.OperationTimeout >= options.SafetyMargin {
		return Options{}, errors.New("zidk8s: OperationTimeout must be shorter than SafetyMargin")
	}
	return options, nil
}

func (m *Manager) Acquire(ctx context.Context, request zid.WorkerIDRequest) (zid.WorkerIDLease, error) {
	token, err := newOwnerToken()
	if err != nil {
		return nil, err
	}
	owner := m.identity + "/" + token

	for workerID := uint32(0); workerID <= request.MaxWorkerID; workerID++ {
		if request.Excludes(workerID) {
			continue
		}
		leaseName := fmt.Sprintf("%s%d", m.options.LeaseNamePrefix, workerID)
		lease, err := m.client.Get(ctx, leaseName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			candidate := m.newLease(leaseName, owner)
			started := candidate.Spec.RenewTime.Time
			created, createErr := m.client.Create(ctx, candidate, metav1.CreateOptions{})
			if createErr == nil {
				return m.startLease(workerID, created.Name, owner, started)
			}
			if apierrors.IsAlreadyExists(createErr) || apierrors.IsConflict(createErr) {
				continue
			}
			return nil, fmt.Errorf("zidk8s: create Lease %s: %w", leaseName, createErr)
		}
		if err != nil {
			return nil, fmt.Errorf("zidk8s: get Lease %s: %w", leaseName, err)
		}
		if !leaseAvailable(lease, time.Now()) {
			continue
		}

		updated := lease.DeepCopy()
		now := metav1.NewMicroTime(time.Now().UTC())
		updated.Spec.HolderIdentity = &owner
		updated.Spec.LeaseDurationSeconds = durationSeconds(m.options.TTL)
		updated.Spec.AcquireTime = &now
		updated.Spec.RenewTime = &now
		transitions := int32(1)
		if lease.Spec.LeaseTransitions != nil {
			transitions = *lease.Spec.LeaseTransitions + 1
		}
		updated.Spec.LeaseTransitions = &transitions
		updated, err = m.client.Update(ctx, updated, metav1.UpdateOptions{})
		if err == nil {
			return m.startLease(workerID, updated.Name, owner, now.Time)
		}
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			continue
		}
		return nil, fmt.Errorf("zidk8s: claim Lease %s: %w", leaseName, err)
	}
	return nil, fmt.Errorf("zidk8s: all eligible worker IDs [0,%d] are occupied", request.MaxWorkerID)
}

func (m *Manager) newLease(name, owner string) *coordinationv1.Lease {
	now := metav1.NewMicroTime(time.Now().UTC())
	transitions := int32(0)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: m.namespace},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &owner,
			LeaseDurationSeconds: durationSeconds(m.options.TTL),
			AcquireTime:          &now,
			RenewTime:            &now,
			LeaseTransitions:     &transitions,
		},
	}
}

func (m *Manager) startLease(workerID uint32, name, owner string, started time.Time) (*workerLease, error) {
	lease := &workerLease{
		client:   m.client,
		options:  m.options,
		workerID: workerID,
		name:     name,
		owner:    owner,
		started:  started,
		lost:     make(chan struct{}),
		stop:     make(chan struct{}),
	}
	lease.safeUntil.Store(int64(m.options.TTL - m.options.SafetyMargin))
	if !lease.Valid() {
		lease.lose()
		return nil, fmt.Errorf("zidk8s: acquire Lease %s exceeded the local safety window", name)
	}
	go lease.run()
	return lease, nil
}

func leaseAvailable(lease *coordinationv1.Lease, now time.Time) bool {
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return true
	}
	if lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds <= 0 {
		return false
	}
	expiresAt := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
	return !now.Before(expiresAt)
}

func durationSeconds(duration time.Duration) *int32 {
	seconds := int32((duration + time.Second - 1) / time.Second)
	return &seconds
}

type workerLease struct {
	client   clientcoordinationv1.LeaseInterface
	options  Options
	workerID uint32
	name     string
	owner    string

	started   time.Time
	safeUntil atomic.Int64
	state     atomic.Uint32
	lost      chan struct{}
	stop      chan struct{}
	loseOnce  sync.Once
	stopOnce  sync.Once
}

func (l *workerLease) WorkerID() uint32      { return l.workerID }
func (l *workerLease) Lost() <-chan struct{} { return l.lost }
func (l *workerLease) State() zid.LeaseState {
	if !l.Valid() {
		return zid.LeaseLost
	}
	return zid.LeaseState(l.state.Load())
}
func (l *workerLease) Valid() bool {
	if zid.LeaseState(l.state.Load()) == zid.LeaseLost {
		return false
	}
	if time.Since(l.started) >= time.Duration(l.safeUntil.Load()) {
		return false
	}
	select {
	case <-l.lost:
		return false
	default:
		return true
	}
}

func (l *workerLease) Close() error {
	l.stopOnce.Do(func() { close(l.stop) })
	l.lose()
	return nil
}

func (l *workerLease) run() {
	deadline := l.started.Add(time.Duration(l.safeUntil.Load()))
	timer := time.NewTimer(renewDelay(l.options.RenewInterval, time.Until(deadline)))
	defer timer.Stop()

	for {
		select {
		case <-l.stop:
			return
		case <-timer.C:
		}

		if !time.Now().Before(deadline) {
			l.lose()
			return
		}

		renewStarted := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), l.options.OperationTimeout)
		renewed, err := l.renew(ctx)
		cancel()
		if err == nil && !renewed {
			l.lose()
			return
		}
		if err == nil && time.Now().Before(deadline) {
			deadline = renewStarted.Add(l.options.TTL - l.options.SafetyMargin)
			l.safeUntil.Store(int64(deadline.Sub(l.started)))
			l.state.Store(uint32(zid.LeaseHealthy))
			timer.Reset(renewDelay(l.options.RenewInterval, time.Until(deadline)))
			continue
		}
		l.state.Store(uint32(zid.LeaseDegraded))

		remaining := time.Until(deadline)
		if remaining <= 0 {
			l.lose()
			return
		}
		timer.Reset(renewDelay(time.Second, remaining))
	}
}

func renewDelay(preferred, remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	if preferred < remaining {
		return preferred
	}
	delay := remaining / 2
	if delay <= 0 {
		return remaining
	}
	return delay
}

func (l *workerLease) renew(ctx context.Context) (bool, error) {
	lease, err := l.client.Get(ctx, l.name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != l.owner {
		return false, nil
	}

	updated := lease.DeepCopy()
	now := metav1.NewMicroTime(time.Now().UTC())
	updated.Spec.RenewTime = &now
	updated.Spec.LeaseDurationSeconds = durationSeconds(l.options.TTL)
	if _, err := l.client.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return false, err
	}
	return true, nil
}

func (l *workerLease) lose() {
	l.loseOnce.Do(func() {
		l.state.Store(uint32(zid.LeaseLost))
		close(l.lost)
	})
}

func newOwnerToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("zidk8s: create owner token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}
