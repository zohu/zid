package zidredis

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/zohu/zid"
)

type fakeClient struct {
	mu                 sync.Mutex
	owners             map[string]string
	setErr             error
	renewErr           error
	renewOwnerMismatch bool
	renewals           int
	setDelay           time.Duration
}

func newFakeClient() *fakeClient {
	return &fakeClient{owners: make(map[string]string)}
}

func (c *fakeClient) setNX(ctx context.Context, key, owner string, _ time.Duration) (bool, error) {
	if c.setDelay > 0 {
		select {
		case <-time.After(c.setDelay):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setErr != nil {
		return false, c.setErr
	}
	if _, exists := c.owners[key]; exists {
		return false, nil
	}
	c.owners[key] = owner
	return true, nil
}

func (c *fakeClient) renew(ctx context.Context, key, owner string, _ time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renewals++
	if c.renewErr != nil {
		return false, c.renewErr
	}
	if c.renewOwnerMismatch || c.owners[key] != owner {
		return false, nil
	}
	return true, nil
}

func testOptions() Options {
	return Options{
		Prefix:           "test:zid:",
		TTL:              100 * time.Millisecond,
		RenewInterval:    20 * time.Millisecond,
		SafetyMargin:     50 * time.Millisecond,
		OperationTimeout: 10 * time.Millisecond,
	}
}

func TestNewManagerValidation(t *testing.T) {
	var nilClient redis.UniversalClient
	if _, err := New(nilClient, Options{}); err == nil {
		t.Fatal("nil Redis client should fail")
	}
	var typedNilClient *redis.Client
	if _, err := New(typedNilClient, Options{}); err == nil {
		t.Fatal("typed nil Redis client should fail")
	}
	client := newFakeClient()
	if _, err := newManager(client, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, options := range []Options{
		{Prefix: ":"},
		{TTL: time.Second, SafetyMargin: time.Second},
		{TTL: time.Second, SafetyMargin: 500 * time.Millisecond, RenewInterval: 500 * time.Millisecond},
		{TTL: time.Second, SafetyMargin: 100 * time.Millisecond, RenewInterval: 50 * time.Millisecond, OperationTimeout: 100 * time.Millisecond},
	} {
		if _, err := newManager(client, options); err == nil {
			t.Fatalf("newManager(%+v) unexpectedly succeeded", options)
		}
	}
}

func TestAcquireSkipsOccupiedWorkerID(t *testing.T) {
	client := newFakeClient()
	client.owners["test:zid:0"] = "another-owner"
	manager, err := newManager(client, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{MaxWorkerID: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.WorkerID() != 1 {
		t.Fatalf("WorkerID = %d, want 1", lease.WorkerID())
	}
	client.mu.Lock()
	owner := client.owners["test:zid:1"]
	client.mu.Unlock()
	if len(owner) != 32 {
		t.Fatalf("owner token length = %d, want 32", len(owner))
	}
}

func TestAcquireSkipsExcludedWorkerID(t *testing.T) {
	client := newFakeClient()
	manager, err := newManager(client, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{
		MaxWorkerID:       3,
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

func TestAcquireSafetyDeadlineStartsBeforeRedisRequest(t *testing.T) {
	client := newFakeClient()
	client.setDelay = 25 * time.Millisecond
	options := testOptions()
	options.TTL = time.Second
	options.SafetyMargin = 500 * time.Millisecond
	options.RenewInterval = 200 * time.Millisecond
	options.OperationTimeout = 100 * time.Millisecond
	manager, err := newManager(client, options)
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
		t.Fatalf("safety clock started after Redis returned: delay=%v", workerLease.started.Sub(before))
	}
}

func TestAcquireRejectsLeaseAfterSafetyWindow(t *testing.T) {
	client := newFakeClient()
	client.setDelay = 60 * time.Millisecond
	manager, err := newManager(client, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{}); err == nil {
		t.Fatal("acquisition beyond the safety window unexpectedly succeeded")
	}
}

func TestAcquireErrors(t *testing.T) {
	client := newFakeClient()
	client.setErr = errors.New("redis offline")
	manager, _ := newManager(client, testOptions())
	if _, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{MaxWorkerID: 3}); err == nil {
		t.Fatal("Redis error should be returned")
	}

	client.setErr = nil
	for workerID := 0; workerID <= 3; workerID++ {
		client.owners[manager.options.Prefix+":"+string(rune('0'+workerID))] = "occupied"
	}
	if _, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{MaxWorkerID: 3}); err == nil {
		t.Fatal("exhausted worker IDs should fail")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Acquire(ctx, zid.WorkerIDRequest{MaxWorkerID: 3}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestOwnerMismatchPermanentlyLosesLease(t *testing.T) {
	client := newFakeClient()
	client.renewOwnerMismatch = true
	manager, _ := newManager(client, testOptions())
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !lease.Valid() {
		t.Fatal("new lease should be valid")
	}
	defer lease.Close()
	waitForLost(t, lease.Lost(), 200*time.Millisecond)
	if lease.Valid() {
		t.Fatal("lost lease should be invalid")
	}
}

func TestRenewalErrorsLoseLeaseAtSafetyDeadline(t *testing.T) {
	client := newFakeClient()
	client.renewErr = errors.New("temporary outage")
	manager, _ := newManager(client, testOptions())
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	waitForLost(t, lease.Lost(), 200*time.Millisecond)
}

func TestRenewalErrorReportsDegradedBeforeLoss(t *testing.T) {
	client := newFakeClient()
	client.renewErr = errors.New("temporary outage")
	manager, _ := newManager(client, testOptions())
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
	client := newFakeClient()
	manager, _ := newManager(client, testOptions())
	lease, err := manager.Acquire(context.Background(), zid.WorkerIDRequest{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(45 * time.Millisecond)
	select {
	case <-lease.Lost():
		t.Fatal("lease was lost after successful renewals")
	default:
	}
	client.mu.Lock()
	renewals := client.renewals
	client.mu.Unlock()
	if renewals == 0 {
		t.Fatal("expected at least one renewal")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	waitForLost(t, lease.Lost(), time.Second)
}

func TestOwnerTokensAreUnique(t *testing.T) {
	first, err := newOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("owner tokens must be unique")
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
