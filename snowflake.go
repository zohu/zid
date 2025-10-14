package zid

import (
	"sync"
	"time"
)

type Snowflake struct {
	baseTime          int64  // 基础时间
	workerId          int64  // 机器码
	workerIdBitLength byte   // 机器码位长
	seqBitLength      byte   // 自增序列数位长
	maxSeqNumber      uint32 // 最大序列数（含）
	minSeqNumber      uint32 // 最小序列数（含）
	topOverCostCount  uint32 // 最大漂移次数

	timestampShift         byte
	currentSeqNumber       uint32
	lastTimeTick           int64
	turnBackTimeTick       int64
	turnBackIndex          byte
	isOverCost             bool
	overCostCountInOneTerm uint32

	sync.Mutex
}

func NewSnowflake(options *Options) ISnowflake {
	return &Snowflake{
		baseTime:          options.BaseTime,
		workerIdBitLength: options.WorkerIdBitLength,
		workerId:          options.WorkerId,
		seqBitLength:      options.SeqBitLength,
		maxSeqNumber:      options.maxSeqNumber(),
		minSeqNumber:      options.MinSeqNumber,
		topOverCostCount:  options.TopOverCostCount,
		timestampShift:    options.timeShift(),
		currentSeqNumber:  options.MinSeqNumber,

		lastTimeTick:           0,
		turnBackTimeTick:       0,
		turnBackIndex:          0,
		isOverCost:             false,
		overCostCountInOneTerm: 0,
	}
}
func (sw *Snowflake) NextId() int64 {
	sw.Lock()
	defer sw.Unlock()
	if sw.isOverCost {
		return sw.NextOverCostId()
	} else {
		return sw.NextNormalId()
	}
}
func (sw *Snowflake) ExtractTime(id int64) time.Time {
	return time.UnixMilli(id>>(sw.timestampShift) + sw.baseTime)
}
func (sw *Snowflake) ExtractWorkerId(id int64) int64 {
	id >>= sw.seqBitLength
	mask := int64((1 << sw.workerIdBitLength) - 1)
	return id & mask
}
func (sw *Snowflake) NextOverCostId() int64 {
	currentTimeTick := sw.GetCurrentTimeTick()
	if currentTimeTick > sw.lastTimeTick {
		sw.lastTimeTick = currentTimeTick
		sw.currentSeqNumber = sw.minSeqNumber
		sw.isOverCost = false
		sw.overCostCountInOneTerm = 0
		return sw.CalcId(sw.lastTimeTick)
	}
	if sw.overCostCountInOneTerm >= sw.topOverCostCount {
		sw.lastTimeTick = sw.GetNextTimeTick()
		sw.currentSeqNumber = sw.minSeqNumber
		sw.isOverCost = false
		sw.overCostCountInOneTerm = 0
		return sw.CalcId(sw.lastTimeTick)
	}
	if sw.currentSeqNumber > sw.maxSeqNumber {
		sw.lastTimeTick++
		sw.currentSeqNumber = sw.minSeqNumber
		sw.isOverCost = true
		sw.overCostCountInOneTerm++
		return sw.CalcId(sw.lastTimeTick)
	}
	return sw.CalcId(sw.lastTimeTick)
}
func (sw *Snowflake) NextNormalId() int64 {
	currentTimeTick := sw.GetCurrentTimeTick()
	if currentTimeTick < sw.lastTimeTick {
		if sw.turnBackTimeTick < 1 {
			sw.turnBackTimeTick = sw.lastTimeTick - 1
			sw.turnBackIndex++
			if sw.turnBackIndex > 4 {
				sw.turnBackIndex = 1
			}
		}
		return sw.CalcTurnBackId(sw.turnBackTimeTick)
	}

	if sw.turnBackTimeTick > 0 {
		sw.turnBackTimeTick = 0
	}
	if currentTimeTick > sw.lastTimeTick {
		sw.lastTimeTick = currentTimeTick
		sw.currentSeqNumber = sw.minSeqNumber
		return sw.CalcId(sw.lastTimeTick)
	}
	if sw.currentSeqNumber > sw.maxSeqNumber {
		sw.lastTimeTick++
		sw.currentSeqNumber = sw.minSeqNumber
		sw.isOverCost = true
		sw.overCostCountInOneTerm = 1
		return sw.CalcId(sw.lastTimeTick)
	}
	return sw.CalcId(sw.lastTimeTick)
}
func (sw *Snowflake) CalcId(useTimeTick int64) int64 {
	result := useTimeTick<<sw.timestampShift + sw.workerId<<sw.seqBitLength + int64(sw.currentSeqNumber)
	sw.currentSeqNumber++
	return result
}
func (sw *Snowflake) CalcTurnBackId(useTimeTick int64) int64 {
	result := useTimeTick<<sw.timestampShift + sw.workerId<<sw.seqBitLength + int64(sw.turnBackIndex)
	sw.turnBackTimeTick--
	return result
}
func (sw *Snowflake) GetCurrentTimeTick() int64 {
	return time.Now().UnixMilli() - sw.baseTime
}
func (sw *Snowflake) GetNextTimeTick() int64 {
	tempTimeTicker := sw.GetCurrentTimeTick()
	for tempTimeTicker <= sw.lastTimeTick {
		time.Sleep(time.Millisecond)
		tempTimeTicker = sw.GetCurrentTimeTick()
	}
	return tempTimeTicker
}
