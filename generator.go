package zid

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const clockPollInterval = 100 * time.Microsecond

const (
	recoveryInitialBackoff = 100 * time.Millisecond
	recoveryMaxBackoff     = 5 * time.Second
)

var errLeaseLost = errors.New("zid: worker ID lease is no longer safe")

// LeaseState describes the coordinator-facing health of a worker-ID lease.
type LeaseState uint8

const (
	LeaseHealthy LeaseState = iota
	LeaseDegraded
	LeaseLost
)

func (state LeaseState) String() string {
	switch state {
	case LeaseHealthy:
		return "healthy"
	case LeaseDegraded:
		return "degraded"
	case LeaseLost:
		return "lost"
	default:
		return fmt.Sprintf("LeaseState(%d)", state)
	}
}

// GeneratorState describes whether a Generator can emit IDs now.
type GeneratorState uint8

const (
	GeneratorHealthy GeneratorState = iota
	GeneratorDegraded
	GeneratorRecovering
	GeneratorClosed
)

func (state GeneratorState) String() string {
	switch state {
	case GeneratorHealthy:
		return "healthy"
	case GeneratorDegraded:
		return "degraded"
	case GeneratorRecovering:
		return "recovering"
	case GeneratorClosed:
		return "closed"
	default:
		return fmt.Sprintf("GeneratorState(%d)", state)
	}
}

// WorkerIDRequest describes the worker-ID namespace available to a manager.
// ExcludedWorkerIDs must not be returned, even if their coordinator entries
// appear free. Generators use this during recovery to avoid uncertain reuse.
type WorkerIDRequest struct {
	MaxWorkerID       uint32
	ExcludedWorkerIDs []uint32
}

// Excludes reports whether a worker ID is unavailable for this acquisition.
func (r WorkerIDRequest) Excludes(workerID uint32) bool {
	for _, excluded := range r.ExcludedWorkerIDs {
		if workerID == excluded {
			return true
		}
	}
	return false
}

// WorkerIDLease represents an exclusive, self-renewing worker-ID lease.
// Valid is the authoritative hot-path safety check. Lost is closed when the
// lease has been permanently fenced.
type WorkerIDLease interface {
	WorkerID() uint32
	Valid() bool
	State() LeaseState
	Lost() <-chan struct{}
	Close() error
}

// WorkerIDManager acquires a worker ID from a distributed coordinator.
type WorkerIDManager interface {
	Acquire(ctx context.Context, request WorkerIDRequest) (WorkerIDLease, error)
}

type leaseHolder struct{ lease WorkerIDLease }

// Generator is an immutable, concurrency-safe ID generator.
type Generator struct {
	config   config
	shards   []*snowflake
	mask     uint32
	clock    clock
	managed  bool
	manager  WorkerIDManager
	lease    atomic.Pointer[leaseHolder]
	mode     atomic.Uint32
	closed   atomic.Bool
	closedCh chan struct{}
	lifetime context.Context
	cancel   context.CancelFunc

	waitMu sync.Mutex
	waitCh chan struct{}

	recoveryMu       sync.Mutex
	recoveryCh       chan struct{}
	excludedWorkerID []uint32
}

// New creates an independent process-local generator.
func New(options Options) (*Generator, error) {
	return newGenerator(options, nil, systemClock{})
}

// MustNew is New for applications that treat invalid startup configuration as
// unrecoverable.
func MustNew(options Options) *Generator {
	generator, err := New(options)
	if err != nil {
		panic(err)
	}
	return generator
}

// NewManaged acquires a worker ID before returning a distributed generator.
// Initialization errors are reported here; NextID remains an error-free hot
// path. If the lease is later lost, generation is fenced while the Generator
// automatically acquires a different worker ID.
func NewManaged(ctx context.Context, manager WorkerIDManager, options Options) (*Generator, error) {
	if isNilInterface(manager) {
		return nil, fmt.Errorf("zid: WorkerIDManager must not be nil")
	}
	if options.WorkerID != 0 {
		return nil, fmt.Errorf("zid: managed generators assign WorkerID; leave it at zero")
	}

	cfg, err := normalizeOptions(options, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if cfg.workerIDBits == 0 {
		return nil, fmt.Errorf("zid: managed generators require WorkerIDBits > 0")
	}

	lease, err := manager.Acquire(ctx, WorkerIDRequest{MaxWorkerID: cfg.maxWorkerID})
	if err != nil {
		return nil, fmt.Errorf("zid: acquire worker ID: %w", err)
	}
	if isNilInterface(lease) {
		return nil, fmt.Errorf("zid: WorkerIDManager returned a nil lease")
	}
	if !lease.Valid() {
		_ = lease.Close()
		return nil, fmt.Errorf("zid: WorkerIDManager returned an invalid lease")
	}
	options.WorkerID = lease.WorkerID()
	options.DisableWorkerID = false

	generator, err := newGenerator(options, lease, systemClock{})
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	if !lease.Valid() {
		_ = generator.Close()
		return nil, fmt.Errorf("zid: worker ID lease expired during generator construction")
	}
	generator.manager = manager
	generator.watchLease(generator.lease.Load())
	return generator, nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func newGenerator(options Options, lease WorkerIDLease, source clock) (*Generator, error) {
	cfg, err := normalizeOptions(options, source.nowMillis())
	if err != nil {
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())
	generator := &Generator{
		config:   cfg,
		shards:   make([]*snowflake, cfg.shardCount),
		mask:     cfg.shardCount - 1,
		clock:    source,
		managed:  lease != nil,
		closedCh: make(chan struct{}),
		lifetime: lifetime,
		cancel:   cancel,
	}
	if lease != nil {
		generator.lease.Store(&leaseHolder{lease: lease})
	}
	for shardID := uint32(0); shardID < cfg.shardCount; shardID++ {
		generator.shards[shardID] = newSnowflake(cfg, shardID, source)
	}
	return generator, nil
}

// NextID returns a unique ID. It blocks while a managed generator replaces a
// lost safety lease, because returning a value before recovery would violate
// the uniqueness contract. Use NextIDContext when the caller needs cancellation.
func (g *Generator) NextID() int64 {
	id, err := g.nextID(context.Background())
	if err == nil {
		return id
	}
	if err == ErrClosed {
		panic(err)
	}
	if err == errLeaseLost {
		panic("zid: managed recovery stopped unexpectedly")
	}
	panic(err)
}

// NextIDContext is the cancellable counterpart of NextID.
func (g *Generator) NextIDContext(ctx context.Context) (int64, error) {
	if ctx == nil {
		return 0, fmt.Errorf("zid: context must not be nil")
	}
	return g.nextID(ctx)
}

func (g *Generator) nextID(ctx context.Context) (int64, error) {
nextAttempt:
	for {
		start := uint32(0)
		if g.mask != 0 {
			start = rand.Uint32() & g.mask
		}

		id, earliestTick, err := g.shards[start].tryNextID(ctx, g)
		if err != nil {
			handled := g.handleRecoverableError(ctx, err)
			if handled == nil {
				continue nextAttempt
			}
			return 0, handled
		}
		if earliestTick == shardAvailable {
			return id, nil
		}

		if earliestTick >= 0 {
			for offset := uint32(1); offset < uint32(len(g.shards)); offset++ {
				shardID := (start + offset) & g.mask
				id, exhaustedAt, err := g.shards[shardID].tryNextID(ctx, g)
				if err != nil {
					handled := g.handleRecoverableError(ctx, err)
					if handled == nil {
						continue nextAttempt
					}
					return 0, handled
				}
				if exhaustedAt == shardAvailable {
					return id, nil
				}
				if exhaustedAt < earliestTick {
					earliestTick = exhaustedAt
				}
			}
		}

		if err := g.waitForTick(ctx, earliestTick); err != nil {
			handled := g.handleRecoverableError(ctx, err)
			if handled != nil {
				return 0, handled
			}
		}
	}
}

func (g *Generator) handleRecoverableError(ctx context.Context, err error) error {
	if !errors.Is(err, errLeaseLost) || g.manager == nil {
		return err
	}
	g.beginRecovery(g.lease.Load())
	return g.waitForRecovery(ctx)
}

// waitForTick coalesces capacity waits. The first caller polls the clock while
// every other caller waits on the same channel. Closing the channel wakes all
// followers so they can retry every shard at the new real tick.
func (g *Generator) waitForTick(ctx context.Context, exhaustedAt int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := g.gateError(); err != nil {
		return err
	}
	if g.clock.nowMillis()-g.config.baseTime > exhaustedAt {
		return nil
	}

	g.waitMu.Lock()
	if g.waitCh != nil {
		waitCh := g.waitCh
		g.waitMu.Unlock()
		return g.waitForLeader(ctx, waitCh)
	}
	waitCh := make(chan struct{})
	g.waitCh = waitCh
	g.waitMu.Unlock()

	err := g.pollClock(ctx, exhaustedAt)
	g.waitMu.Lock()
	if g.waitCh == waitCh {
		g.waitCh = nil
		close(waitCh)
	}
	g.waitMu.Unlock()
	return err
}

func (g *Generator) waitForLeader(ctx context.Context, waitCh <-chan struct{}) error {
	var lost <-chan struct{}
	if holder := g.lease.Load(); holder != nil {
		lost = holder.lease.Lost()
	}
	select {
	case <-waitCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-g.closedCh:
		return ErrClosed
	case <-lost:
		return errLeaseLost
	}
}

func (g *Generator) pollClock(ctx context.Context, exhaustedAt int64) error {
	for g.clock.nowMillis()-g.config.baseTime <= exhaustedAt {
		if err := g.gateError(); err != nil {
			return err
		}
		if err := g.clock.wait(ctx, clockPollInterval); err != nil {
			return err
		}
	}
	return nil
}

func (g *Generator) gateError() error {
	if g.closed.Load() {
		return ErrClosed
	}
	if !g.managed {
		return nil
	}
	if GeneratorState(g.mode.Load()) == GeneratorRecovering {
		return errLeaseLost
	}
	holder := g.lease.Load()
	if holder == nil {
		return nil
	}
	if !holder.lease.Valid() {
		return errLeaseLost
	}
	select {
	case <-holder.lease.Lost():
		return errLeaseLost
	default:
		return nil
	}
}

// State reports the current generation and lease lifecycle state.
func (g *Generator) State() GeneratorState {
	if g.closed.Load() {
		return GeneratorClosed
	}
	if GeneratorState(g.mode.Load()) == GeneratorRecovering {
		return GeneratorRecovering
	}
	holder := g.lease.Load()
	if holder != nil {
		switch holder.lease.State() {
		case LeaseDegraded:
			return GeneratorDegraded
		case LeaseLost:
			g.beginRecovery(holder)
			return GeneratorRecovering
		}
	}
	return GeneratorHealthy
}

// Healthy reports whether the generator can safely produce IDs now. A
// degraded lease remains safe until its local safety deadline.
func (g *Generator) Healthy() bool {
	err := g.gateError()
	if errors.Is(err, errLeaseLost) && g.manager != nil {
		g.beginRecovery(g.lease.Load())
	}
	return err == nil
}

// Close permanently closes the generator. It is safe to call more than once.
func (g *Generator) Close() error {
	if !g.closed.CompareAndSwap(false, true) {
		return nil
	}
	g.cancel()
	close(g.closedCh)

	// Serialize with installRecoveredLease. If recovery has already installed a
	// replacement, Close observes and closes it; otherwise installation sees the
	// closed flag and rejects the replacement.
	g.recoveryMu.Lock()
	g.mode.Store(uint32(GeneratorClosed))
	holder := g.lease.Load()
	g.recoveryMu.Unlock()
	if holder != nil {
		return holder.lease.Close()
	}
	return nil
}

func (g *Generator) watchLease(holder *leaseHolder) {
	if holder == nil || g.manager == nil {
		return
	}
	go func() {
		select {
		case <-holder.lease.Lost():
			g.beginRecovery(holder)
		case <-g.lifetime.Done():
		}
	}()
}

func (g *Generator) beginRecovery(expected *leaseHolder) {
	if expected == nil || g.manager == nil || g.closed.Load() {
		return
	}

	g.recoveryMu.Lock()
	if g.closed.Load() || g.lease.Load() != expected || GeneratorState(g.mode.Load()) == GeneratorRecovering {
		g.recoveryMu.Unlock()
		return
	}
	if !containsWorkerID(g.excludedWorkerID, expected.lease.WorkerID()) {
		g.excludedWorkerID = append(g.excludedWorkerID, expected.lease.WorkerID())
	}
	g.recoveryCh = make(chan struct{})
	g.mode.Store(uint32(GeneratorRecovering))
	recoveryCh := g.recoveryCh
	g.recoveryMu.Unlock()

	go g.recoverWorkerID(expected, recoveryCh)
}

func (g *Generator) waitForRecovery(ctx context.Context) error {
	for {
		if g.closed.Load() {
			return ErrClosed
		}
		g.recoveryMu.Lock()
		if GeneratorState(g.mode.Load()) != GeneratorRecovering {
			g.recoveryMu.Unlock()
			return nil
		}
		recoveryCh := g.recoveryCh
		g.recoveryMu.Unlock()

		select {
		case <-recoveryCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-g.closedCh:
			return ErrClosed
		}
	}
}

func (g *Generator) recoverWorkerID(expected *leaseHolder, recoveryCh chan struct{}) {
	backoff := recoveryInitialBackoff
	for {
		g.recoveryMu.Lock()
		excluded := append([]uint32(nil), g.excludedWorkerID...)
		g.recoveryMu.Unlock()

		lease, err := g.manager.Acquire(g.lifetime, WorkerIDRequest{
			MaxWorkerID:       g.config.maxWorkerID,
			ExcludedWorkerIDs: excluded,
		})
		if err == nil && !isNilInterface(lease) && lease.Valid() && lease.WorkerID() <= g.config.maxWorkerID && !containsWorkerID(excluded, lease.WorkerID()) {
			if g.installRecoveredLease(expected, recoveryCh, lease) {
				return
			}
			_ = lease.Close()
			return
		}
		if !isNilInterface(lease) {
			_ = lease.Close()
		}

		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-g.lifetime.Done():
			timer.Stop()
			return
		}
		backoff = min(backoff*2, recoveryMaxBackoff)
	}
}

func (g *Generator) installRecoveredLease(expected *leaseHolder, recoveryCh chan struct{}, lease WorkerIDLease) bool {
	for _, shard := range g.shards {
		shard.mu.Lock()
	}

	g.recoveryMu.Lock()
	if g.closed.Load() || g.lease.Load() != expected || g.recoveryCh != recoveryCh {
		g.recoveryMu.Unlock()
		for index := len(g.shards) - 1; index >= 0; index-- {
			g.shards[index].mu.Unlock()
		}
		return false
	}

	workerID := lease.WorkerID()
	for shardID, shard := range g.shards {
		shard.nodePart = int64(workerID) << (g.config.shardBits + g.config.sequenceBits)
		shard.nodePart |= int64(shardID) << g.config.sequenceBits
		shard.lastTick = -1
		shard.sequence = 0
	}
	holder := &leaseHolder{lease: lease}
	g.lease.Store(holder)
	g.mode.Store(uint32(GeneratorHealthy))
	g.recoveryCh = nil
	close(recoveryCh)
	g.recoveryMu.Unlock()

	for index := len(g.shards) - 1; index >= 0; index-- {
		g.shards[index].mu.Unlock()
	}
	_ = expected.lease.Close()
	g.watchLease(holder)
	return true
}

func containsWorkerID(workerIDs []uint32, workerID uint32) bool {
	for _, candidate := range workerIDs {
		if candidate == workerID {
			return true
		}
	}
	return false
}

func (g *Generator) NextString() string { return strconv.FormatInt(g.NextID(), 10) }
func (g *Generator) NextHex() string    { return strconv.FormatInt(g.NextID(), 16) }
func (g *Generator) NextBase36() string { return strconv.FormatInt(g.NextID(), 36) }
func (g *Generator) NextBase62() string { return toBase62(g.NextID()) }

func (g *Generator) ExtractTime(id int64) time.Time {
	return time.UnixMilli((id >> g.config.timestampShift) + g.config.baseTime).UTC()
}

func (g *Generator) ExtractWorkerID(id int64) uint32 {
	shift := g.config.shardBits + g.config.sequenceBits
	return uint32(id>>shift) & g.config.maxWorkerID
}

func (g *Generator) ExtractShardID(id int64) uint32 {
	return uint32(id>>g.config.sequenceBits) & bitMask(g.config.shardBits)
}
