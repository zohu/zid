package zid

import (
	"time"

	_ "unsafe"
)

//go:linkname fastrand runtime.fastrand
func fastrand() uint32

type ShardedGenerator struct {
	shards []ISnowflake
	mask   uint32 // = maxShards - 1
}

func NewShardedGenerator(options *Options) ISnowflake {
	options = firstTruth(options, new(Options))
	if err := options.Validate(); err != nil {
		panic(err)
	}
	maxShards := options.MaxWorkerIdNumber()
	gen := &ShardedGenerator{
		shards: make([]ISnowflake, maxShards),
		mask:   uint32(maxShards) - 1,
	}
	baseOpts := *options
	for i := int64(0); i < maxShards; i++ {
		opt := baseOpts
		opt.WorkerId = i // 唯一 WorkerId
		gen.shards[i] = NewSnowflake(&opt)
	}
	return gen
}

func (s *ShardedGenerator) NextId() int64 {
	idx := fastrand() & s.mask
	return s.shards[idx].NextId()
}
func (s *ShardedGenerator) ExtractTime(id int64) time.Time {
	return s.shards[0].ExtractTime(id)
}
func (s *ShardedGenerator) ExtractWorkerId(id int64) int64 {
	return s.shards[0].ExtractWorkerId(id)
}
