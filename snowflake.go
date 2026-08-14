package zid

import (
	"context"
	"sync"
	"time"
)

type clock interface {
	nowMillis() int64
	wait(context.Context, time.Duration) error
}

type systemClock struct{}

const shardAvailable = int64(-2)

func (systemClock) nowMillis() int64 { return time.Now().UnixMilli() }
func (systemClock) wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type snowflake struct {
	mu sync.Mutex

	clock          clock
	baseTime       int64
	nodePart       int64
	timestampShift uint8
	maxSequence    uint32

	lastTick int64
	sequence uint32
}

func newSnowflake(cfg config, shardID uint32, source clock) *snowflake {
	nodePart := int64(cfg.workerID) << (cfg.shardBits + cfg.sequenceBits)
	nodePart |= int64(shardID) << cfg.sequenceBits
	return &snowflake{
		clock:          source,
		baseTime:       cfg.baseTime,
		nodePart:       nodePart,
		timestampShift: cfg.timestampShift,
		maxSequence:    cfg.maxSequence,
		lastTick:       -1,
	}
}

// tryNextID never advances logical time beyond wall time. It returns
// shardAvailable with a generated ID, -1 while the clock is before BaseTime,
// or the real tick that must be exceeded before an exhausted shard can resume.
func (s *snowflake) tryNextID(ctx context.Context, generator *Generator) (int64, int64, error) {
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return 0, 0, err
	}
	if err := generator.gateError(); err != nil {
		s.mu.Unlock()
		return 0, 0, err
	}

	tick := s.clock.nowMillis() - s.baseTime
	if tick < 0 {
		s.mu.Unlock()
		return 0, -1, nil
	}

	if tick > s.lastTick {
		s.lastTick = tick
		s.sequence = 0
	} else if s.sequence > s.maxSequence {
		exhaustedAt := s.lastTick
		s.mu.Unlock()
		return 0, exhaustedAt, nil
	}

	id := s.lastTick<<s.timestampShift | s.nodePart | int64(s.sequence)
	if id < 0 {
		s.mu.Unlock()
		panic("zid: signed int64 timestamp range exhausted")
	}
	s.sequence++
	s.mu.Unlock()
	return id, shardAvailable, nil
}
