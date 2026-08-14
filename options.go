package zid

import (
	"errors"
	"fmt"
	"math"
)

const (
	defaultWorkerIDBits = uint8(4)
	defaultSequenceBits = uint8(18)
	maxLayoutBits       = uint8(22)
	maxShardBits        = uint8(16)
	// DefaultBaseTime is 2026-08-01 00:00:00 UTC in Unix milliseconds.
	DefaultBaseTime = int64(1785542400000)
)

var (
	ErrClosed = errors.New("zid: generator is closed")
)

// Options defines the immutable bit layout of a Generator.
//
// The zero value is valid and uses DefaultOptions. Set DisableWorkerID when a
// process-local generator should spend no bits on a worker ID. ShardBits adds
// local shards without consuming the WorkerID namespace.
type Options struct {
	BaseTime        int64
	WorkerID        uint32
	WorkerIDBits    uint8
	ShardBits       uint8
	SequenceBits    uint8
	DisableWorkerID bool
}

// DefaultOptions returns the recommended high-throughput single-generator
// layout: 16 workers and 262,144 IDs per millisecond per worker.
func DefaultOptions() Options {
	return Options{
		BaseTime:     DefaultBaseTime,
		WorkerIDBits: defaultWorkerIDBits,
		SequenceBits: defaultSequenceBits,
	}
}

type config struct {
	baseTime       int64
	workerID       uint32
	workerIDBits   uint8
	shardBits      uint8
	sequenceBits   uint8
	timestampShift uint8
	maxWorkerID    uint32
	maxSequence    uint32
	shardCount     uint32
}

func normalizeOptions(options Options, now int64) (config, error) {
	if options == (Options{}) {
		options = DefaultOptions()
	} else {
		if options.BaseTime == 0 {
			options.BaseTime = DefaultBaseTime
		}
		if options.SequenceBits == 0 {
			options.SequenceBits = defaultSequenceBits
		}
		if options.DisableWorkerID && (options.WorkerID != 0 || options.WorkerIDBits != 0) {
			return config{}, errors.New("zid: DisableWorkerID cannot be combined with WorkerID or WorkerIDBits")
		}
		if options.DisableWorkerID {
			options.WorkerID = 0
			options.WorkerIDBits = 0
		} else if options.WorkerIDBits == 0 {
			options.WorkerIDBits = defaultWorkerIDBits
		}
	}

	if options.BaseTime <= 0 || options.BaseTime > now {
		return config{}, fmt.Errorf("zid: BaseTime must be in (0, now], got %d", options.BaseTime)
	}
	if options.WorkerIDBits > 19 {
		return config{}, fmt.Errorf("zid: WorkerIDBits must be between 0 and 19, got %d", options.WorkerIDBits)
	}
	if options.ShardBits > maxShardBits {
		return config{}, fmt.Errorf("zid: ShardBits must be between 0 and %d, got %d", maxShardBits, options.ShardBits)
	}
	if options.SequenceBits < 3 || options.SequenceBits > maxLayoutBits {
		return config{}, fmt.Errorf("zid: SequenceBits must be between 3 and %d, got %d", maxLayoutBits, options.SequenceBits)
	}

	layoutBits := options.WorkerIDBits + options.ShardBits + options.SequenceBits
	if layoutBits > maxLayoutBits {
		return config{}, fmt.Errorf("zid: WorkerIDBits + ShardBits + SequenceBits must be <= %d, got %d", maxLayoutBits, layoutBits)
	}

	maxWorkerID := bitMask(options.WorkerIDBits)
	if options.WorkerID > maxWorkerID {
		return config{}, fmt.Errorf("zid: WorkerID must be <= %d, got %d", maxWorkerID, options.WorkerID)
	}

	maxTick := int64(math.MaxInt64 >> layoutBits)
	if now-options.BaseTime > maxTick {
		return config{}, errors.New("zid: BaseTime and bit layout have exhausted the signed int64 timestamp range")
	}

	return config{
		baseTime:       options.BaseTime,
		workerID:       options.WorkerID,
		workerIDBits:   options.WorkerIDBits,
		shardBits:      options.ShardBits,
		sequenceBits:   options.SequenceBits,
		timestampShift: layoutBits,
		maxWorkerID:    maxWorkerID,
		maxSequence:    bitMask(options.SequenceBits),
		shardCount:     uint32(1) << options.ShardBits,
	}, nil
}

func bitMask(bits uint8) uint32 {
	if bits == 0 {
		return 0
	}
	return uint32(1)<<bits - 1
}
