package zid

import (
	"strconv"
	"time"
)

var idGenerator ISnowflake

func init() {
	if idGenerator == nil {
		WithOptions(nil)
	}
}

func WithOptions(options *Options) {
	options = firstTruth(options, new(Options))
	if err := options.Validate(); err != nil {
		panic(err)
	}
	if options.ShardedMode {
		idGenerator = NewShardedGenerator(options)
	} else {
		idGenerator = NewDefaultIdGenerator(options)
	}
}

// NextInt
// @Description: 10进制ID
// @return int64
func NextInt() int64 {
	return idGenerator.NextId()
}

// NextString
// @Description: 10进制ID字符串
// @return string
func NextString() string {
	return strconv.FormatInt(NextInt(), 10)
}

// NextHex
// @Description: 16进制ID字符串
// @return string
func NextHex() string {
	return strconv.FormatInt(NextInt(), 16)
}

// NextBase36
// @Description: 适用于不区分大小写场景
// @return string
func NextBase36() string {
	return strconv.FormatInt(NextInt(), 36)
}

// NextBase62
// @Description: 适用于区分大小写场景
// @return string
func NextBase62() string {
	return toBase62(NextInt())
}

// ExtractTime
// @Description: 提取ID时间
// @param id
// @return time.Time
func ExtractTime(id int64) time.Time {
	return idGenerator.ExtractTime(id)
}
func ExtractTimeHex(hex string) time.Time {
	id, _ := strconv.ParseInt(hex, 16, 64)
	return idGenerator.ExtractTime(id)
}
func ExtractTimeBase36(base36 string) time.Time {
	id, _ := strconv.ParseInt(base36, 36, 64)
	return idGenerator.ExtractTime(id)
}
func ExtractTimeBase62(base62 string) time.Time {
	id, _ := fromBase62(base62)
	return idGenerator.ExtractTime(id)
}

// ExtractWorkerId
// @Description: 提取工作节点ID
// @param id
// @return int64
func ExtractWorkerId(id int64) int64 {
	return idGenerator.ExtractWorkerId(id)
}
func ExtractWorkerIdHex(hex string) int64 {
	id, _ := strconv.ParseInt(hex, 16, 64)
	return idGenerator.ExtractWorkerId(id)
}
func ExtractWorkerIdBase36(base36 string) int64 {
	id, _ := strconv.ParseInt(base36, 36, 64)
	return idGenerator.ExtractWorkerId(id)
}
func ExtractWorkerIdBase62(base62 string) int64 {
	id, _ := fromBase62(base62)
	return idGenerator.ExtractWorkerId(id)
}
