package zid

import (
	"time"
)

type DefaultIdGenerator struct {
	SnowWorker ISnowflake
}

func NewDefaultIdGenerator(options *Options) ISnowflake {
	options = firstTruth(options, new(Options))
	if err := options.Validate(); err != nil {
		panic(err)
	}
	return &DefaultIdGenerator{
		SnowWorker: NewSnowflake(options),
	}
}

func (dig DefaultIdGenerator) NextId() int64 {
	return dig.SnowWorker.NextId()
}
func (dig DefaultIdGenerator) ExtractTime(id int64) time.Time {
	return dig.SnowWorker.ExtractTime(id)
}
func (dig DefaultIdGenerator) ExtractWorkerId(id int64) int64 {
	return dig.SnowWorker.ExtractWorkerId(id)
}
