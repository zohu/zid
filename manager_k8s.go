package zid

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcoordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

type KubernetesOptions struct {
	PodUID          string
	Namespace       string
	LeaseNamePrefix string
	Config          *rest.Config
}

type KubernetesManager struct {
	options   *KubernetesOptions
	client    clientcoordinationv1.LeaseInterface
	workerId  int64
	leaseName string
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

func NewKubernetesManager(opts *KubernetesOptions) WorkerIdManager {
	if opts == nil {
		panic("options can not be nil")
	}
	if opts.LeaseNamePrefix == "" {
		opts.LeaseNamePrefix = "snowflake-"
	}
	opts.PodUID = firstTruth(opts.PodUID, os.Getenv("POD_UID"))
	if opts.PodUID == "" {
		panic("pod uid can not be empty")
	}
	opts.Namespace = firstTruth(opts.Namespace, os.Getenv("NAMESPACE"))
	if opts.Namespace == "" {
		panic("namespace can not be empty")
	}
	if opts.Config == nil {
		config, err := rest.InClusterConfig()
		if err != nil {
			panic(fmt.Sprintf("in-cluster config failed: %v", err))
		}
		opts.Config = config
	}

	clientset, err := kubernetes.NewForConfig(opts.Config)
	if err != nil {
		panic(err)
	}
	return &KubernetesManager{
		options: opts,
		client:  clientset.CoordinationV1().Leases(opts.Namespace),
		stopCh:  make(chan struct{}),
	}
}

func (m *KubernetesManager) Acquire(ctx context.Context, max int64) error {
	for id := int64(0); id <= max; id++ {
		leaseName := m.options.LeaseNamePrefix + strconv.FormatInt(id, 10)

		lease, err := m.client.Get(ctx, leaseName, metav1.GetOptions{})
		if err != nil {
			newLease := &coordinationv1.Lease{
				ObjectMeta: metav1.ObjectMeta{Name: leaseName},
				Spec: coordinationv1.LeaseSpec{
					HolderIdentity:       &m.options.PodUID,
					LeaseDurationSeconds: m.ptr(expirationDurationSec),
					RenewTime:            &metav1.MicroTime{Time: time.Now()},
				},
			}
			_, err = m.client.Create(ctx, newLease, metav1.CreateOptions{})
			if err == nil {
				m.workerId = id
				m.leaseName = leaseName
				slog.Info(fmt.Sprintf("Acquired workerId %d via new lease", id))
				return nil
			}
			continue
		}
		if lease.Spec.RenewTime != nil {
			expiry := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
			if time.Now().After(expiry) {
				// 尝试抢占过期 Lease
				lease.Spec.HolderIdentity = &m.options.PodUID
				lease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
				_, err = m.client.Update(ctx, lease, metav1.UpdateOptions{})
				if err == nil {
					m.workerId = id
					m.leaseName = leaseName
					slog.Info(fmt.Sprintf("Acquired workerId %d via new lease", id))
					return nil
				}
			}
		}
	}
	return fmt.Errorf("all worker id [0-%d] are occupied, please extend WorkerIdBitLength", max)
}
func (m *KubernetesManager) StartRenewal() {
	ticker := time.NewTicker(time.Duration(renewIntervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			lease, err := m.client.Get(ctx, m.leaseName, metav1.GetOptions{})
			if err == nil && lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == m.options.PodUID {
				lease.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
				_, _ = m.client.Update(ctx, lease, metav1.UpdateOptions{})
			}
			cancel()
		case <-m.stopCh:
			close(m.stopCh)
			return
		}
	}
}
func (m *KubernetesManager) Stop() {
	m.stopCh <- struct{}{}
}
func (m *KubernetesManager) GetWorkerId() int64 {
	return m.workerId
}

func (m *KubernetesManager) ptr(x int) *int32 {
	y := int32(x)
	return &y
}
