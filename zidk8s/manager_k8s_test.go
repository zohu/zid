package zidk8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zohu/zid"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientcoordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	clienttesting "k8s.io/client-go/testing"
)

func testOptions() Options {
	return Options{
		Namespace:        "test-namespace",
		Identity:         "test-pod",
		LeaseNamePrefix:  "test-zid-",
		TTL:              100 * time.Millisecond,
		RenewInterval:    20 * time.Millisecond,
		SafetyMargin:     50 * time.Millisecond,
		OperationTimeout: 10 * time.Millisecond,
	}
}

func TestNormalizeOptions(t *testing.T) {
	t.Setenv("NAMESPACE", "")
	t.Setenv("POD_UID", "")
	options, err := normalizeOptions(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if options.LeaseNamePrefix != "test-zid-" {
		t.Fatalf("prefix = %q", options.LeaseNamePrefix)
	}

	tests := []Options{
		{Identity: "pod"},
		{Namespace: "ns"},
		{Namespace: "ns", Identity: "pod", LeaseNamePrefix: "-"},
		{Namespace: "ns", Identity: "pod", TTL: time.Second, SafetyMargin: time.Second},
		{Namespace: "ns", Identity: "pod", TTL: time.Duration(1<<31) * time.Second, SafetyMargin: time.Second, RenewInterval: time.Second, OperationTimeout: time.Millisecond},
		{Namespace: "ns", Identity: "pod", TTL: time.Second, SafetyMargin: 500 * time.Millisecond, RenewInterval: 500 * time.Millisecond},
		{Namespace: "ns", Identity: "pod", TTL: time.Second, SafetyMargin: 100 * time.Millisecond, RenewInterval: 50 * time.Millisecond, OperationTimeout: 100 * time.Millisecond},
	}
	for _, invalid := range tests {
		if _, err := normalizeOptions(invalid); err == nil {
			t.Fatalf("normalizeOptions(%+v) unexpectedly succeeded", invalid)
		}
	}
}

func TestNewManagerRejectsNilClient(t *testing.T) {
	var client clientcoordinationv1.LeaseInterface
	if _, err := newManager(client, testOptions()); err == nil {
		t.Fatal("nil LeaseInterface should fail")
	}
}

func TestAcquireCreatesLease(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{MaxWorkerID: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.WorkerID() != 0 {
		t.Fatalf("WorkerID = %d, want 0", lease.WorkerID())
	}
	if !lease.Valid() {
		t.Fatal("new lease should be valid")
	}
	stored, err := clientset.CoordinationV1().Leases("test-namespace").Get(context.Background(), "test-zid-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec.HolderIdentity == nil || !strings.HasPrefix(*stored.Spec.HolderIdentity, "test-pod/") {
		t.Fatalf("holder identity = %v", stored.Spec.HolderIdentity)
	}
	if stored.Spec.AcquireTime == nil || stored.Spec.RenewTime == nil || stored.Spec.LeaseDurationSeconds == nil {
		t.Fatalf("incomplete Lease spec: %+v", stored.Spec)
	}
}

func TestAcquireSafetyDeadlineStartsBeforeKubernetesRequest(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "leases", func(clienttesting.Action) (bool, runtime.Object, error) {
		time.Sleep(25 * time.Millisecond)
		return false, nil, nil
	})
	options := testOptions()
	options.TTL = time.Second
	options.SafetyMargin = 500 * time.Millisecond
	options.RenewInterval = 200 * time.Millisecond
	options.OperationTimeout = 100 * time.Millisecond
	manager, err := newManager(clientset.CoordinationV1().Leases(options.Namespace), options)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	workerLease := lease.(*workerLease)
	if workerLease.started.Sub(before) > 5*time.Millisecond {
		t.Fatalf("safety clock started after Kubernetes returned: delay=%v", workerLease.started.Sub(before))
	}
}

func TestAcquireRejectsLeaseAfterSafetyWindow(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "leases", func(clienttesting.Action) (bool, runtime.Object, error) {
		time.Sleep(60 * time.Millisecond)
		return false, nil, nil
	})
	manager := newTestManager(t, clientset)
	if _, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{}); err == nil {
		t.Fatal("acquisition beyond the safety window unexpectedly succeeded")
	}
}

func TestAcquireSkipsActiveLease(t *testing.T) {
	active := makeLease("test-zid-0", "another", time.Now(), time.Second, 2)
	clientset := fake.NewSimpleClientset(active)
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{MaxWorkerID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.WorkerID() != 1 {
		t.Fatalf("WorkerID = %d, want 1", lease.WorkerID())
	}
}

func TestAcquireSkipsExcludedLease(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{
		MaxWorkerID:       2,
		ExcludedWorkerIDs: []uint32{0, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.WorkerID() != 1 {
		t.Fatalf("WorkerID = %d, want first eligible ID 1", lease.WorkerID())
	}
}

func TestAcquireTakesOverExpiredLease(t *testing.T) {
	expired := makeLease("test-zid-0", "old-owner", time.Now().Add(-2*time.Second), time.Second, 4)
	clientset := fake.NewSimpleClientset(expired)
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	stored, err := clientset.CoordinationV1().Leases("test-namespace").Get(context.Background(), "test-zid-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec.LeaseTransitions == nil || *stored.Spec.LeaseTransitions != 5 {
		t.Fatalf("LeaseTransitions = %v, want 5", stored.Spec.LeaseTransitions)
	}
	if stored.Spec.HolderIdentity == nil || *stored.Spec.HolderIdentity == "old-owner" {
		t.Fatalf("holder was not replaced: %v", stored.Spec.HolderIdentity)
	}
}

func TestMalformedHeldLeaseIsNotClaimed(t *testing.T) {
	holder := "unknown-owner"
	malformed := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "test-zid-0", Namespace: "test-namespace"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &holder},
	}
	clientset := fake.NewSimpleClientset(malformed)
	manager := newTestManager(t, clientset)
	if _, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{}); err == nil {
		t.Fatal("malformed held Lease must not be claimed")
	}
}

func TestAcquireReturnsAPIErrors(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("get", "leases", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("API unavailable")
	})
	manager := newTestManager(t, clientset)
	if _, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{MaxWorkerID: 1}); err == nil {
		t.Fatal("API error should be returned")
	}
}

func TestHolderReplacementLosesLease(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	stored, err := clientset.CoordinationV1().Leases("test-namespace").Get(context.Background(), "test-zid-0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replacement := "replacement"
	stored.Spec.HolderIdentity = &replacement
	if _, err := clientset.CoordinationV1().Leases("test-namespace").Update(context.Background(), stored, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForLost(t, lease.Lost(), 200*time.Millisecond)
	if lease.Valid() {
		t.Fatal("lost lease should be invalid")
	}
}

func TestDeletedLeaseIsLost(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := clientset.CoordinationV1().Leases("test-namespace").Delete(context.Background(), "test-zid-0", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForLost(t, lease.Lost(), 200*time.Millisecond)
}

func TestRenewalErrorReportsDegradedBeforeLoss(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("update", "leases", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("API unavailable")
	})
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()

	deadline := time.Now().Add(45 * time.Millisecond)
	for lease.State() == zid.LeaseHealthy && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state := lease.State(); state != zid.LeaseDegraded {
		t.Fatalf("state = %v, want LeaseDegraded", state)
	}
	if !lease.Valid() {
		t.Fatal("degraded lease should remain valid before its safety deadline")
	}
}

func TestSuccessfulRenewalAndIdempotentClose(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	manager := newTestManager(t, clientset)
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(45 * time.Millisecond)
	select {
	case <-lease.Lost():
		t.Fatal("lease was lost despite successful renewals")
	default:
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	waitForLost(t, lease.Lost(), time.Second)
}

func TestLeaseAvailability(t *testing.T) {
	if !leaseAvailable(&coordinationv1.Lease{}, time.Now()) {
		t.Fatal("empty Lease should be available")
	}
	active := makeLease("lease", "holder", time.Now(), time.Second, 0)
	if leaseAvailable(active, time.Now()) {
		t.Fatal("active Lease should not be available")
	}
	if !leaseAvailable(active, time.Now().Add(2*time.Second)) {
		t.Fatal("expired Lease should be available")
	}
}

func newTestManager(t *testing.T, clientset *fake.Clientset) *Manager {
	t.Helper()
	manager, err := newManager(clientset.CoordinationV1().Leases("test-namespace"), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func makeLease(name, holder string, renewTime time.Time, duration time.Duration, transitions int32) *coordinationv1.Lease {
	renew := metav1.NewMicroTime(renewTime.UTC())
	durationValue := int32(duration / time.Second)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-namespace"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &durationValue,
			RenewTime:            &renew,
			LeaseTransitions:     &transitions,
		},
	}
}

func waitForLost(t *testing.T, lost <-chan struct{}, timeout time.Duration) {
	t.Helper()
	select {
	case <-lost:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for lease loss")
	}
}
