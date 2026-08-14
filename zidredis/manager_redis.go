package zidredis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zohu/zid"
)

const renewScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`

// Options controls Redis-backed worker-ID leases.
type Options struct {
	Prefix           string
	TTL              time.Duration
	RenewInterval    time.Duration
	SafetyMargin     time.Duration
	OperationTimeout time.Duration
}

func DefaultOptions() Options {
	return Options{
		Prefix:           "zid",
		TTL:              30 * time.Second,
		RenewInterval:    10 * time.Second,
		SafetyMargin:     10 * time.Second,
		OperationTimeout: 3 * time.Second,
	}
}

type commandClient interface {
	setNX(context.Context, string, string, time.Duration) (bool, error)
	renew(context.Context, string, string, time.Duration) (bool, error)
}

type redisCommands struct{ client redis.UniversalClient }

func (c redisCommands) setNX(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return c.client.SetNX(ctx, key, owner, ttl).Result()
}

func (c redisCommands) renew(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	result, err := c.client.Eval(ctx, renewScript, []string{key}, owner, ttl.Milliseconds()).Int64()
	return result == 1, err
}

// Manager allocates exclusive worker IDs through Redis.
type Manager struct {
	client  commandClient
	options Options
}

func New(client redis.UniversalClient, options Options) (*Manager, error) {
	if isNilInterface(client) {
		return nil, errors.New("zidredis: client must not be nil")
	}
	return newManager(redisCommands{client: client}, options)
}

// ConfigureDefault constructs a Redis manager and installs it as zid's
// package-level managed Generator. Call it once during process startup, before
// any zid package-level generation or extraction helper is used.
func ConfigureDefault(ctx context.Context, client redis.UniversalClient, managerOptions Options, generatorOptions zid.Options) error {
	manager, err := New(client, managerOptions)
	if err != nil {
		return err
	}
	return zid.ConfigureDefault(ctx, manager, generatorOptions)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func newManager(client commandClient, options Options) (*Manager, error) {
	if options == (Options{}) {
		options = DefaultOptions()
	} else {
		defaults := DefaultOptions()
		if options.Prefix == "" {
			options.Prefix = defaults.Prefix
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
	}
	options.Prefix = strings.TrimSuffix(options.Prefix, ":")
	if options.Prefix == "" {
		return nil, errors.New("zidredis: Prefix must not be empty")
	}
	if options.TTL <= 0 || options.SafetyMargin <= 0 || options.SafetyMargin >= options.TTL {
		return nil, errors.New("zidredis: require 0 < SafetyMargin < TTL")
	}
	if options.RenewInterval <= 0 || options.RenewInterval >= options.TTL-options.SafetyMargin {
		return nil, errors.New("zidredis: RenewInterval must be shorter than the usable lease duration")
	}
	if options.OperationTimeout <= 0 || options.OperationTimeout >= options.SafetyMargin {
		return nil, errors.New("zidredis: OperationTimeout must be shorter than SafetyMargin")
	}
	return &Manager{client: client, options: options}, nil
}

func (m *Manager) Acquire(ctx context.Context, request zid.WorkerIDRequest) (zid.WorkerIDLease, error) {
	owner, err := newOwnerToken()
	if err != nil {
		return nil, err
	}
	for workerID := uint32(0); workerID <= request.MaxWorkerID; workerID++ {
		if request.Excludes(workerID) {
			continue
		}
		key := fmt.Sprintf("%s:%d", m.options.Prefix, workerID)
		started := time.Now()
		acquired, err := m.client.setNX(ctx, key, owner, m.options.TTL)
		if err != nil {
			return nil, fmt.Errorf("zidredis: acquire %s: %w", key, err)
		}
		if !acquired {
			continue
		}

		lease := &workerLease{
			client:   m.client,
			options:  m.options,
			workerID: workerID,
			key:      key,
			owner:    owner,
			started:  started,
			lost:     make(chan struct{}),
			stop:     make(chan struct{}),
		}
		lease.safeUntil.Store(int64(m.options.TTL - m.options.SafetyMargin))
		if !lease.Valid() {
			lease.lose()
			return nil, fmt.Errorf("zidredis: acquire %s exceeded the local safety window", key)
		}
		go lease.run()
		return lease, nil
	}
	return nil, fmt.Errorf("zidredis: all eligible worker IDs [0,%d] are occupied", request.MaxWorkerID)
}

type workerLease struct {
	client   commandClient
	options  Options
	workerID uint32
	key      string
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
		renewed, err := l.client.renew(ctx, l.key, l.owner, l.options.TTL)
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
		retry := renewDelay(time.Second, remaining)
		timer.Reset(retry)
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

func (l *workerLease) lose() {
	l.loseOnce.Do(func() {
		l.state.Store(uint32(zid.LeaseLost))
		close(l.lost)
	})
}

func newOwnerToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("zidredis: create owner token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}
