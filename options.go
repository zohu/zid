package zid

import (
	"fmt"
	"reflect"
	"strconv"
	"time"
)

type ISnowflake interface {
	NextId() int64
	ExtractTime(int64) time.Time
	ExtractWorkerId(id int64) int64
}
type Options struct {
	BaseTime          int64  // 基础时间（ms单位），不能超过当前系统时间
	WorkerId          int64  // 机器码，最大值 2^WorkerIdBitLength-1
	WorkerIdBitLength byte   // 机器码位长，默认值4，取值范围 [1~19,f]（要求：序列数位长+机器码位长不超过22）
	SeqBitLength      byte   // 序列数位长，默认值6，取值范围 [3~21,22]（要求：序列数位长+机器码位长不超过22）
	MaxSeqNumber      uint32 // 最大序列数（含），设置范围 [MinSeqNumber, 2^SeqBitLength-1]，默认值0，表示最大序列数取最大值（2^SeqBitLength-1]）
	MinSeqNumber      uint32 // 最小序列数（含），默认值5，取值范围 [5, MaxSeqNumber]，每毫秒的前5个序列数对应编号0-4是保留位，其中1-4是时间回拨相应预留位，0是手工新值预留位
	TopOverCostCount  uint32 // 最大漂移次数（含），默认2000，推荐范围500-10000（与计算能力有关）
	ShardedMode       bool   // 单机高性能模式，默认false，如果开启，WorkerId将被忽略，WorkerIdBitLength用来控制分片量
}

func (o *Options) Validate() error {
	baseTime := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	o.BaseTime = firstTruth(o.BaseTime, baseTime)
	o.WorkerIdBitLength = firstTruth(o.WorkerIdBitLength, 4)
	o.SeqBitLength = firstTruth(o.SeqBitLength, 6)
	o.MinSeqNumber = firstTruth(o.MinSeqNumber, 5)
	o.TopOverCostCount = firstTruth(o.TopOverCostCount, 2000)

	if o.WorkerIdBitLength == 'f' {
		o.WorkerIdBitLength = 0
		o.WorkerId = 0
		o.ShardedMode = false
	}

	if o.BaseTime < baseTime || o.BaseTime > time.Now().UnixMilli() {
		return fmt.Errorf("BaseTime range:[2025-01-01 ~ now]")
	}
	if o.WorkerIdBitLength < 0 || o.WorkerIdBitLength > 19 {
		return fmt.Errorf("WorkerIdBitLength range:[1~19,f]")
	}
	if o.WorkerIdBitLength+o.SeqBitLength > 22 {
		return fmt.Errorf("WorkerIdBitLength + SeqBitLength <= 22")
	}
	maxWorkerIdNumber := o.MaxWorkerIdNumber()
	if o.WorkerId < 0 || o.WorkerId > maxWorkerIdNumber {
		return fmt.Errorf("WorkerId range:[0, %s]", strconv.FormatUint(uint64(maxWorkerIdNumber), 10))
	}
	if o.SeqBitLength < 3 || o.SeqBitLength > 22 {
		return fmt.Errorf("SeqBitLength range:[3~21,22]")
	}
	maxSeqNumber := o.maxSeqNumber()
	if o.MaxSeqNumber < 0 || o.MaxSeqNumber > maxSeqNumber {
		return fmt.Errorf("MaxSeqNumber range:[1, %s]", strconv.FormatUint(uint64(maxSeqNumber), 10))
	}
	if o.MinSeqNumber < 5 || o.MinSeqNumber > maxSeqNumber {
		return fmt.Errorf("MinSeqNumber range:[5, %s]", strconv.FormatUint(uint64(maxSeqNumber), 10))
	}
	if o.TopOverCostCount < 0 || o.TopOverCostCount > 10000 {
		return fmt.Errorf("TopOverCostCount range:[0, 10000]")
	}
	return nil
}

func (o *Options) timeShift() byte {
	return o.WorkerIdBitLength + o.SeqBitLength
}

func (o *Options) MaxWorkerIdNumber() int64 {
	return int64(1<<o.WorkerIdBitLength) - 1
}
func (o *Options) maxSeqNumber() uint32 {
	return uint32(1<<o.SeqBitLength) - 1
}
func firstTruth[T any](args ...T) T {
	for _, item := range args {
		if !reflect.ValueOf(item).IsZero() {
			return item
		}
	}
	return args[0]
}
