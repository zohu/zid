package zid

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	now           atomic.Int64
	waits         atomic.Int64
	advanceOnWait bool
	waitGate      <-chan struct{}
}

func newFakeClock(now int64, advanceOnWait bool) *fakeClock {
	clock := &fakeClock{advanceOnWait: advanceOnWait}
	clock.now.Store(now)
	return clock
}

func (c *fakeClock) nowMillis() int64 { return c.now.Load() }
func (c *fakeClock) wait(ctx context.Context, _ time.Duration) error {
	c.waits.Add(1)
	if c.waitGate != nil {
		select {
		case <-c.waitGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.advanceOnWait {
		c.now.Add(1)
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestGeneratorLayoutAndExtraction(t *testing.T) {
	baseTime := DefaultBaseTime
	clock := newFakeClock(baseTime+1234, true)
	generator, err := newGenerator(Options{
		BaseTime:     baseTime,
		WorkerID:     5,
		WorkerIDBits: 3,
		ShardBits:    2,
		SequenceBits: 3,
	}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}

	for shardID, shard := range generator.shards {
		id, exhaustedAt, err := shard.tryNextID(context.Background(), generator)
		if err != nil {
			t.Fatal(err)
		}
		if exhaustedAt != shardAvailable {
			t.Fatal("shard unexpectedly exhausted")
		}
		if got := generator.ExtractWorkerID(id); got != 5 {
			t.Fatalf("worker ID = %d, want 5", got)
		}
		if got := generator.ExtractShardID(id); got != uint32(shardID) {
			t.Fatalf("shard ID = %d, want %d", got, shardID)
		}
		if got := generator.ExtractTime(id); !got.Equal(time.UnixMilli(baseTime + 1234).UTC()) {
			t.Fatalf("time = %s", got)
		}
	}
}

func TestClockRollbackNeverDuplicatesOrBorrowsFutureTime(t *testing.T) {
	clock := newFakeClock(DefaultBaseTime+100, true)
	generator, err := newGenerator(Options{
		DisableWorkerID: true,
		SequenceBits:    3,
	}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[int64]struct{})
	for range 4 {
		seen[generator.NextID()] = struct{}{}
	}
	clock.now.Store(DefaultBaseTime + 99)
	for range 4 {
		id := generator.NextID()
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate ID after rollback: %d", id)
		}
		seen[id] = struct{}{}
		if tick := id >> 3; tick != 100 {
			t.Fatalf("rollback ID tick = %d, want 100", tick)
		}
	}

	id := generator.NextID()
	if tick := id >> 3; tick != 101 {
		t.Fatalf("post-exhaustion tick = %d, want 101", tick)
	}
	if waits := clock.waits.Load(); waits != 2 {
		t.Fatalf("wait count = %d, want 2", waits)
	}
}

func TestSequenceExhaustionHonorsContext(t *testing.T) {
	clock := newFakeClock(DefaultBaseTime+1, false)
	generator, err := newGenerator(Options{DisableWorkerID: true, SequenceBits: 3}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		generator.NextID()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = generator.NextIDContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestShardedGeneratorTriesOtherShardsBeforeWaiting(t *testing.T) {
	clock := newFakeClock(DefaultBaseTime+1, false)
	generator, err := newGenerator(Options{
		DisableWorkerID: true,
		ShardBits:       2,
		SequenceBits:    3,
	}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}

	for shardID := range 3 {
		shard := generator.shards[shardID]
		shard.lastTick = 1
		shard.sequence = shard.maxSequence + 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id, err := generator.NextIDContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if shardID := generator.ExtractShardID(id); shardID != 3 {
		t.Fatalf("shard ID = %d, want the only available shard 3", shardID)
	}
	if waits := clock.waits.Load(); waits != 0 {
		t.Fatalf("clock waits = %d, want 0 while another shard has capacity", waits)
	}
}

func TestCapacityWaitIsCoalesced(t *testing.T) {
	clock := newFakeClock(DefaultBaseTime+1, false)
	generator, err := newGenerator(Options{
		DisableWorkerID: true,
		ShardBits:       2,
		SequenceBits:    3,
	}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range generator.shards {
		shard.lastTick = 1
		shard.sequence = shard.maxSequence + 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	const goroutines = 32
	errorsCh := make(chan error, goroutines)
	for range goroutines {
		go func() {
			_, err := generator.NextIDContext(ctx)
			errorsCh <- err
		}()
	}

	deadline := time.Now().Add(time.Second)
	for clock.waits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if waits := clock.waits.Load(); waits != 1 {
		cancel()
		t.Fatalf("concurrent clock waits = %d, want exactly 1", waits)
	}
	time.Sleep(20 * time.Millisecond)
	if waits := clock.waits.Load(); waits != 1 {
		cancel()
		t.Fatalf("concurrent clock waits = %d after followers joined, want 1", waits)
	}

	cancel()
	for range goroutines {
		if err := <-errorsCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	}
}

func TestSharedCapacityWaitWakesFollowersWithoutDuplicates(t *testing.T) {
	waitGate := make(chan struct{})
	clock := newFakeClock(DefaultBaseTime+1, true)
	clock.waitGate = waitGate
	generator, err := newGenerator(Options{
		DisableWorkerID: true,
		ShardBits:       2,
		SequenceBits:    3,
	}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range generator.shards {
		shard.lastTick = 1
		shard.sequence = shard.maxSequence + 1
	}

	const goroutines = 32
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ids := make(chan int64, goroutines)
	errorsCh := make(chan error, goroutines)
	for range goroutines {
		go func() {
			id, err := generator.NextIDContext(ctx)
			ids <- id
			errorsCh <- err
		}()
	}

	deadline := time.Now().Add(time.Second)
	for clock.waits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if waits := clock.waits.Load(); waits != 1 {
		t.Fatalf("concurrent clock waits = %d, want exactly 1", waits)
	}
	time.Sleep(20 * time.Millisecond)
	if waits := clock.waits.Load(); waits != 1 {
		t.Fatalf("concurrent clock waits = %d after followers joined, want 1", waits)
	}
	close(waitGate)

	seen := make(map[int64]struct{}, goroutines)
	for range goroutines {
		id := <-ids
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate ID after shared wake: %d", id)
		}
		seen[id] = struct{}{}
		if tick := id >> generator.config.timestampShift; tick != 2 {
			t.Fatalf("ID tick = %d, want 2", tick)
		}
	}
	if waits := clock.waits.Load(); waits != 1 {
		t.Fatalf("total clock waits = %d, want 1", waits)
	}
}

func TestConcurrentUniqueness(t *testing.T) {
	generator := MustNew(DefaultOptions())
	const goroutines = 64
	const idsPerGoroutine = 2_000

	ids := make(chan int64, goroutines*idsPerGoroutine)
	var group sync.WaitGroup
	for range goroutines {
		group.Add(1)
		go func() {
			defer group.Done()
			for range idsPerGoroutine {
				ids <- generator.NextID()
			}
		}()
	}
	group.Wait()
	close(ids)

	seen := make(map[int64]struct{}, goroutines*idsPerGoroutine)
	for id := range ids {
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate ID: %d", id)
		}
		seen[id] = struct{}{}
	}
}

func TestShardedConcurrentUniqueness(t *testing.T) {
	generator := MustNew(Options{WorkerID: 3, WorkerIDBits: 4, ShardBits: 8, SequenceBits: 10})
	const count = 100_000
	seen := make(map[int64]struct{}, count)
	seenShards := make(map[uint32]struct{}, 256)
	for range count {
		id := generator.NextID()
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate sharded ID: %d", id)
		}
		seen[id] = struct{}{}
		if generator.ExtractWorkerID(id) != 3 {
			t.Fatalf("wrong worker ID in %d", id)
		}
		seenShards[generator.ExtractShardID(id)] = struct{}{}
	}
	if len(seenShards) != 256 {
		t.Fatalf("reached %d shards, want 256", len(seenShards))
	}
}

type fakeLease struct {
	workerID uint32
	lost     chan struct{}
	state    atomic.Uint32
	closed   atomic.Bool
	loseOnce sync.Once
}

func (l *fakeLease) WorkerID() uint32 { return l.workerID }
func (l *fakeLease) Valid() bool {
	select {
	case <-l.lost:
		return false
	default:
		return true
	}
}
func (l *fakeLease) Lost() <-chan struct{} { return l.lost }
func (l *fakeLease) State() LeaseState {
	if !l.Valid() {
		return LeaseLost
	}
	return LeaseState(l.state.Load())
}
func (l *fakeLease) Close() error {
	l.closed.Store(true)
	l.lose()
	return nil
}
func (l *fakeLease) lose() {
	l.loseOnce.Do(func() {
		l.state.Store(uint32(LeaseLost))
		close(l.lost)
	})
}

type fakeManager struct {
	mu       sync.Mutex
	lease    *fakeLease
	err      error
	request  WorkerIDRequest
	requests []WorkerIDRequest
	acquire  func(context.Context, WorkerIDRequest) (WorkerIDLease, error)
}

func (m *fakeManager) Acquire(ctx context.Context, request WorkerIDRequest) (WorkerIDLease, error) {
	m.mu.Lock()
	m.request = request
	m.requests = append(m.requests, request)
	acquire := m.acquire
	lease := m.lease
	err := m.err
	m.mu.Unlock()
	if acquire != nil {
		return acquire(ctx, request)
	}
	return lease, err
}

func TestManagedGenerator(t *testing.T) {
	first := &fakeLease{workerID: 7, lost: make(chan struct{})}
	second := &fakeLease{workerID: 8, lost: make(chan struct{})}
	var acquisitions atomic.Int64
	manager := &fakeManager{acquire: func(_ context.Context, request WorkerIDRequest) (WorkerIDLease, error) {
		if acquisitions.Add(1) == 1 {
			return first, nil
		}
		if !request.Excludes(7) {
			t.Error("recovery request did not exclude lost worker ID 7")
		}
		return second, nil
	}}
	generator, err := NewManaged(context.Background(), manager, Options{WorkerIDBits: 4, SequenceBits: 18})
	if err != nil {
		t.Fatal(err)
	}
	defer generator.Close()
	manager.mu.Lock()
	maxWorkerID := manager.request.MaxWorkerID
	manager.mu.Unlock()
	if maxWorkerID != 15 {
		t.Fatalf("max worker ID = %d, want 15", maxWorkerID)
	}
	if workerID := generator.ExtractWorkerID(generator.NextID()); workerID != 7 {
		t.Fatalf("worker ID = %d, want 7", workerID)
	}
	if !generator.Healthy() {
		t.Fatal("generator should be healthy")
	}

	first.lose()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id, err := generator.NextIDContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if workerID := generator.ExtractWorkerID(id); workerID != 8 {
		t.Fatalf("recovered worker ID = %d, want 8", workerID)
	}
	if state := generator.State(); state != GeneratorHealthy {
		t.Fatalf("state = %v, want GeneratorHealthy", state)
	}
}

func TestDirectNextIDBlocksDuringRecoveryThenResumes(t *testing.T) {
	first := &fakeLease{workerID: 1, lost: make(chan struct{})}
	second := &fakeLease{workerID: 2, lost: make(chan struct{})}
	allowRecovery := make(chan struct{})
	var acquisitions atomic.Int64
	manager := &fakeManager{acquire: func(ctx context.Context, _ WorkerIDRequest) (WorkerIDLease, error) {
		if acquisitions.Add(1) == 1 {
			return first, nil
		}
		select {
		case <-allowRecovery:
			return second, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	generator, err := NewManaged(context.Background(), manager, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer generator.Close()
	first.lose()

	ids := make(chan int64, 1)
	go func() {
		ids <- generator.NextID()
	}()

	select {
	case id := <-ids:
		t.Fatalf("NextID returned before recovery was allowed: %d", id)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowRecovery)
	select {
	case id := <-ids:
		if workerID := generator.ExtractWorkerID(id); workerID != 2 {
			t.Fatalf("worker ID = %d, want 2", workerID)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked NextID did not resume after recovery")
	}
}

func TestNextIDContextCanCancelDuringRecovery(t *testing.T) {
	first := &fakeLease{workerID: 1, lost: make(chan struct{})}
	var acquisitions atomic.Int64
	manager := &fakeManager{acquire: func(ctx context.Context, _ WorkerIDRequest) (WorkerIDLease, error) {
		if acquisitions.Add(1) == 1 {
			return first, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	generator, err := NewManaged(context.Background(), manager, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer generator.Close()
	first.lose()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = generator.NextIDContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestCloseRejectsOrClosesConcurrentRecoveredLease(t *testing.T) {
	first := &fakeLease{workerID: 1, lost: make(chan struct{})}
	second := &fakeLease{workerID: 2, lost: make(chan struct{})}
	secondAcquired := make(chan struct{})
	allowSecond := make(chan struct{})
	var acquisitions atomic.Int64
	manager := &fakeManager{acquire: func(ctx context.Context, _ WorkerIDRequest) (WorkerIDLease, error) {
		if acquisitions.Add(1) == 1 {
			return first, nil
		}
		close(secondAcquired)
		select {
		case <-allowSecond:
			return second, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	generator, err := NewManaged(context.Background(), manager, Options{})
	if err != nil {
		t.Fatal(err)
	}

	first.lose()
	select {
	case <-secondAcquired:
	case <-time.After(time.Second):
		t.Fatal("recovery did not acquire a replacement")
	}
	// Hold the installation lock so Close can mark the Generator closed while
	// the replacement is in flight.
	generator.recoveryMu.Lock()
	close(allowSecond)

	closed := make(chan error, 1)
	go func() { closed <- generator.Close() }()
	deadline := time.Now().Add(time.Second)
	for !generator.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !generator.closed.Load() {
		generator.recoveryMu.Unlock()
		t.Fatal("Close did not mark the Generator closed")
	}
	generator.recoveryMu.Unlock()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(time.Second)
	for !second.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !second.closed.Load() {
		t.Fatal("replacement lease leaked after concurrent Close")
	}
}

func TestGeneratorReportsDegradedLeaseAsSafe(t *testing.T) {
	lease := &fakeLease{workerID: 1, lost: make(chan struct{})}
	manager := &fakeManager{lease: lease}
	generator, err := NewManaged(context.Background(), manager, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer generator.Close()
	lease.state.Store(uint32(LeaseDegraded))
	if state := generator.State(); state != GeneratorDegraded {
		t.Fatalf("state = %v, want GeneratorDegraded", state)
	}
	if !generator.Healthy() {
		t.Fatal("degraded lease should remain safe before its deadline")
	}
}

func TestManagedGeneratorInitializationErrors(t *testing.T) {
	if _, err := NewManaged(context.Background(), nil, Options{}); err == nil {
		t.Fatal("nil manager should fail")
	}
	var typedNilManager *fakeManager
	if _, err := NewManaged(context.Background(), typedNilManager, Options{}); err == nil {
		t.Fatal("typed nil manager should fail")
	}
	manager := &fakeManager{err: errors.New("offline")}
	if _, err := NewManaged(context.Background(), manager, Options{}); err == nil {
		t.Fatal("manager error should be returned")
	}
	manager = &fakeManager{lease: &fakeLease{lost: make(chan struct{})}}
	if _, err := NewManaged(context.Background(), manager, Options{DisableWorkerID: true, SequenceBits: 22}); err == nil {
		t.Fatal("managed generator without worker bits should fail")
	}
	if _, err := NewManaged(context.Background(), manager, Options{WorkerID: 1, WorkerIDBits: 4, SequenceBits: 18}); err == nil {
		t.Fatal("managed generator with preset worker ID should fail")
	}
	manager = &fakeManager{lease: nil}
	if _, err := NewManaged(context.Background(), manager, Options{}); err == nil {
		t.Fatal("nil lease should fail")
	}
	invalidLease := &fakeLease{lost: make(chan struct{})}
	invalidLease.lose()
	manager = &fakeManager{lease: invalidLease}
	if _, err := NewManaged(context.Background(), manager, Options{}); err == nil {
		t.Fatal("invalid lease should fail")
	}
}

func TestMustNewPanicsOnInvalidOptions(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustNew should panic for invalid options")
		}
	}()
	MustNew(Options{SequenceBits: 23})
}

func TestNextIDContextRejectsNilContext(t *testing.T) {
	generator := MustNew(Options{})
	if _, err := generator.NextIDContext(nil); err == nil {
		t.Fatal("nil context should fail")
	}
}

func TestStateStrings(t *testing.T) {
	if LeaseDegraded.String() != "degraded" || LeaseState(99).String() != "LeaseState(99)" {
		t.Fatal("unexpected LeaseState string")
	}
	if GeneratorRecovering.String() != "recovering" || GeneratorState(99).String() != "GeneratorState(99)" {
		t.Fatal("unexpected GeneratorState string")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	generator := MustNew(Options{})
	if err := generator.Close(); err != nil {
		t.Fatal(err)
	}
	if err := generator.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.NextIDContext(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("error = %v, want ErrClosed", err)
	}

	defer func() {
		recovered := recover()
		err, ok := recovered.(error)
		if !ok || !errors.Is(err, ErrClosed) {
			t.Fatalf("panic = %v, want ErrClosed", recovered)
		}
	}()
	generator.NextID()
}

func TestDefaultRegistryInstallsManagedGeneratorBeforeUse(t *testing.T) {
	local := MustNew(Options{})
	registry := newDefaultRegistry(local)
	lease := &fakeLease{workerID: 6, lost: make(chan struct{})}
	manager := &fakeManager{lease: lease}
	if err := registry.configureManaged(context.Background(), manager, Options{}); err != nil {
		t.Fatal(err)
	}
	defer registry.generator.Load().Close()

	id := registry.current().NextID()
	if workerID := registry.current().ExtractWorkerID(id); workerID != 6 {
		t.Fatalf("worker ID = %d, want 6", workerID)
	}
	if !local.closed.Load() {
		t.Fatal("previous local default was not closed after installation")
	}
	if err := registry.configureManaged(context.Background(), manager, Options{}); !errors.Is(err, ErrDefaultAlreadyUsed) {
		t.Fatalf("second configuration error = %v, want ErrDefaultAlreadyUsed", err)
	}
}

func TestDefaultRegistryRejectsConfigurationAfterUse(t *testing.T) {
	local := MustNew(Options{})
	registry := newDefaultRegistry(local)
	defer local.Close()
	_ = registry.current().NextID()

	manager := &fakeManager{lease: &fakeLease{workerID: 1, lost: make(chan struct{})}}
	if err := registry.configureManaged(context.Background(), manager, Options{}); !errors.Is(err, ErrDefaultAlreadyUsed) {
		t.Fatalf("configuration error = %v, want ErrDefaultAlreadyUsed", err)
	}
}

func TestDefaultRegistryFailedConfigurationCanRetry(t *testing.T) {
	local := MustNew(Options{})
	registry := newDefaultRegistry(local)
	failing := &fakeManager{err: errors.New("offline")}
	if err := registry.configureManaged(context.Background(), failing, Options{}); err == nil {
		t.Fatal("failed manager unexpectedly configured the default")
	}

	lease := &fakeLease{workerID: 2, lost: make(chan struct{})}
	working := &fakeManager{lease: lease}
	if err := registry.configureManaged(context.Background(), working, Options{}); err != nil {
		t.Fatal(err)
	}
	defer registry.generator.Load().Close()
	if workerID := registry.current().ExtractWorkerID(registry.current().NextID()); workerID != 2 {
		t.Fatalf("worker ID = %d, want 2", workerID)
	}
}

func TestDefaultRegistryBlocksHelpersDuringConfiguration(t *testing.T) {
	local := MustNew(Options{})
	registry := newDefaultRegistry(local)
	lease := &fakeLease{workerID: 4, lost: make(chan struct{})}
	allowAcquire := make(chan struct{})
	manager := &fakeManager{acquire: func(ctx context.Context, _ WorkerIDRequest) (WorkerIDLease, error) {
		select {
		case <-allowAcquire:
			return lease, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}

	configured := make(chan error, 1)
	go func() {
		configured <- registry.configureManaged(context.Background(), manager, Options{})
	}()
	deadline := time.Now().Add(time.Second)
	for registry.state.Load() != defaultConfiguring && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	ids := make(chan int64, 1)
	go func() { ids <- registry.current().NextID() }()
	select {
	case id := <-ids:
		t.Fatalf("helper returned before configuration completed: %d", id)
	case <-time.After(20 * time.Millisecond):
	}
	close(allowAcquire)
	if err := <-configured; err != nil {
		t.Fatal(err)
	}
	defer registry.generator.Load().Close()
	select {
	case id := <-ids:
		if workerID := registry.current().ExtractWorkerID(id); workerID != 4 {
			t.Fatalf("worker ID = %d, want 4", workerID)
		}
	case <-time.After(time.Second):
		t.Fatal("helper did not resume after configuration")
	}
}

func TestPackageHelpers(t *testing.T) {
	id := NextID()
	if id < 0 {
		t.Fatal("package helpers returned a negative ID")
	}
	if NextString() == "" || NextHex() == "" || NextBase36() == "" || NextBase62() == "" {
		t.Fatal("string helper returned an empty ID")
	}
	if ExtractTime(id).Before(time.UnixMilli(DefaultBaseTime)) {
		t.Fatal("extracted time predates BaseTime")
	}
	if ExtractShardID(id) != 0 {
		t.Fatal("default generator should not encode a shard ID")
	}
	if _, err := strconv.ParseInt(NextString(), 10, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := NextIDContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNextIDDoesNotAllocate(t *testing.T) {
	generator := MustNew(DefaultOptions())
	if allocations := testing.AllocsPerRun(1_000, func() { _ = generator.NextID() }); allocations != 0 {
		t.Fatalf("allocations per NextID = %f, want 0", allocations)
	}
}

func TestPackageNextIDDoesNotAllocate(t *testing.T) {
	if allocations := testing.AllocsPerRun(1_000, func() { _ = NextID() }); allocations != 0 {
		t.Fatalf("allocations per package NextID = %f, want 0", allocations)
	}
}

func BenchmarkGeneratorSingle(b *testing.B) {
	generator := MustNew(DefaultOptions())
	b.ReportAllocs()
	for b.Loop() {
		_ = generator.NextID()
	}
}

func BenchmarkPackageNextID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = NextID()
	}
}

func BenchmarkGeneratorShardedParallel(b *testing.B) {
	generator := MustNew(Options{WorkerIDBits: 4, ShardBits: 8, SequenceBits: 10})
	b.ReportAllocs()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			_ = generator.NextID()
		}
	})
}
