package zid

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var ErrDefaultAlreadyUsed = errors.New("zid: default generator is already configured or has been used")

const (
	defaultPristine uint32 = iota
	defaultLocalUsed
	defaultConfiguring
	defaultManaged
)

type defaultRegistry struct {
	generator atomic.Pointer[Generator]
	state     atomic.Uint32
	mu        sync.Mutex
	ready     chan struct{}
}

func newDefaultRegistry(generator *Generator) *defaultRegistry {
	registry := &defaultRegistry{}
	registry.generator.Store(generator)
	return registry
}

var packageDefault = newDefaultRegistry(MustNew(Options{}))

// ConfigureDefault installs a distributed default Generator exactly once,
// before any package-level helper is used. The explicit startup call keeps
// manager construction free of surprising global side effects.
func ConfigureDefault(ctx context.Context, manager WorkerIDManager, options Options) error {
	return packageDefault.configureManaged(ctx, manager, options)
}

// DefaultState reports the lifecycle state of the package-level Generator.
func DefaultState() GeneratorState { return packageDefault.generator.Load().State() }

// CloseDefault closes the package-level Generator. Package-level Next methods
// panic if used after this call.
func CloseDefault() error { return packageDefault.generator.Load().Close() }

func (r *defaultRegistry) configureManaged(ctx context.Context, manager WorkerIDManager, options Options) error {
	if !r.state.CompareAndSwap(defaultPristine, defaultConfiguring) {
		return ErrDefaultAlreadyUsed
	}

	r.mu.Lock()
	r.ready = make(chan struct{})
	ready := r.ready
	r.mu.Unlock()

	generator, err := NewManaged(ctx, manager, options)
	if err != nil {
		r.state.Store(defaultPristine)
		r.mu.Lock()
		close(ready)
		if r.ready == ready {
			r.ready = nil
		}
		r.mu.Unlock()
		return err
	}

	previous := r.generator.Swap(generator)
	r.state.Store(defaultManaged)
	r.mu.Lock()
	close(ready)
	if r.ready == ready {
		r.ready = nil
	}
	r.mu.Unlock()
	return previous.Close()
}

func (r *defaultRegistry) current() *Generator {
	for {
		switch r.state.Load() {
		case defaultPristine:
			if r.state.CompareAndSwap(defaultPristine, defaultLocalUsed) {
				return r.generator.Load()
			}
		case defaultConfiguring:
			r.mu.Lock()
			ready := r.ready
			r.mu.Unlock()
			if ready == nil {
				runtime.Gosched()
				continue
			}
			<-ready
		default:
			return r.generator.Load()
		}
	}
}

// Package-level helpers use a process-local generator with WorkerID 0. Use New
// or call ConfigureDefault at startup when multiple processes generate.
func NextID() int64      { return packageDefault.current().NextID() }
func NextString() string { return packageDefault.current().NextString() }
func NextHex() string    { return packageDefault.current().NextHex() }
func NextBase36() string { return packageDefault.current().NextBase36() }
func NextBase62() string { return packageDefault.current().NextBase62() }

// NextIDContext is the cancellable counterpart of the package-level NextID.
func NextIDContext(ctx context.Context) (int64, error) {
	return packageDefault.current().NextIDContext(ctx)
}

func ExtractTime(id int64) time.Time  { return packageDefault.current().ExtractTime(id) }
func ExtractWorkerID(id int64) uint32 { return packageDefault.current().ExtractWorkerID(id) }
func ExtractShardID(id int64) uint32  { return packageDefault.current().ExtractShardID(id) }

func ParseHex(value string) (int64, error)    { return parseInt(value, 16) }
func ParseBase36(value string) (int64, error) { return parseInt(value, 36) }
func ParseBase62(value string) (int64, error) { return fromBase62(value) }

func ExtractTimeHex(value string) (time.Time, error) {
	id, err := ParseHex(value)
	if err != nil {
		return time.Time{}, err
	}
	return ExtractTime(id), nil
}

func ExtractTimeBase36(value string) (time.Time, error) {
	id, err := ParseBase36(value)
	if err != nil {
		return time.Time{}, err
	}
	return ExtractTime(id), nil
}

func ExtractTimeBase62(value string) (time.Time, error) {
	id, err := ParseBase62(value)
	if err != nil {
		return time.Time{}, err
	}
	return ExtractTime(id), nil
}

func ExtractWorkerIDHex(value string) (uint32, error) {
	id, err := ParseHex(value)
	if err != nil {
		return 0, err
	}
	return ExtractWorkerID(id), nil
}

func ExtractWorkerIDBase36(value string) (uint32, error) {
	id, err := ParseBase36(value)
	if err != nil {
		return 0, err
	}
	return ExtractWorkerID(id), nil
}

func ExtractWorkerIDBase62(value string) (uint32, error) {
	id, err := ParseBase62(value)
	if err != nil {
		return 0, err
	}
	return ExtractWorkerID(id), nil
}

func parseInt(value string, base int) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("zid: empty base-%d ID", base)
	}
	id, err := strconv.ParseInt(value, base, 64)
	if err != nil {
		return 0, fmt.Errorf("zid: parse base-%d ID: %w", base, err)
	}
	if id < 0 {
		return 0, fmt.Errorf("zid: ID must not be negative")
	}
	return id, nil
}
