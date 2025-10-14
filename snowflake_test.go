package zid

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// 覆盖测试
func Test_Zid(t *testing.T) {
	// 覆盖测试
	WithOptions(&Options{})
	id := NextInt()
	t.Log(fmt.Sprintf("int64  id=%d, len=%d, time=%s, workerId=%d", id, len(strconv.Itoa(int(id))), ExtractTime(id), ExtractWorkerId(id)))

	hex := NextHex()
	t.Log(fmt.Sprintf("hex    id=%s, len=%d, time=%s, workerId=%d", hex, len(hex), ExtractTimeHex(hex), ExtractWorkerIdHex(hex)))

	base36 := NextBase36()
	t.Log(fmt.Sprintf("base36 id=%s, len=%d, time=%s, workerId=%d", base36, len(base36), ExtractTimeBase36(base36), ExtractWorkerIdBase36(base36)))

	base62 := NextBase62()
	t.Log(fmt.Sprintf("base62 id=%s, len=%d, time=%s, workerId=%d", base62, len(base62), ExtractTimeBase62(base62), ExtractWorkerIdBase62(base62)))
}

// TestZidConcurrentUniqueness
// @Description: ID重复测试
// @param t
func TestZidConcurrentUniqueness(t *testing.T) {
	WithOptions(&Options{
		ShardedMode: false,
	})
	const goroutines = 200
	const idsPerGoroutine = 1000

	var wg sync.WaitGroup
	seen := make(map[int64]bool)
	var mu sync.Mutex
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localSeen := make(map[int64]bool)
			for j := 0; j < idsPerGoroutine; j++ {
				id := NextInt()
				if localSeen[id] {
					errCh <- fmt.Errorf("duplicate ID in same goroutine: %d", id)
					return
				}
				localSeen[id] = true

				mu.Lock()
				if seen[id] {
					mu.Unlock()
					errCh <- fmt.Errorf("global duplicate ID: %d", id)
					return
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

// benchCase
// @Description: 性能测试

type benchCase struct {
	name                 string
	workerBits           byte
	seqBits              byte
	sharded              bool
	concurrentGoroutines int // 仅用于 Parallel 测试
}

var benchCases = []benchCase{
	// 非分片（单实例，锁竞争）
	{"Single_4_18", 4, 18, false, 1},
	{"Single_6_16", 6, 16, false, 1},
	{"Single_8_14", 8, 14, false, 1},
	{"Single_f_22", 'f', 22, false, 1},

	// 分片模式（多实例，无锁）
	{"Shard_8_14_10g", 8, 14, true, 10},
	{"Shard_10_12_50g", 10, 12, true, 50},
	{"Shard_12_10_100g", 12, 10, true, 100},
	{"Shard_16_6_100g", 16, 6, true, 100},
	{"Shard_16_6_100g", 16, 6, true, 1},
}

func runBenchmark(b *testing.B, bc benchCase) {
	opts := &Options{
		WorkerIdBitLength: bc.workerBits,
		SeqBitLength:      bc.seqBits,
		ShardedMode:       bc.sharded,
	}
	WithOptions(opts)
	b.ReportAllocs()

	if bc.concurrentGoroutines <= 1 {
		// 串行
		for i := 0; i < b.N; i++ {
			_ = NextInt()
		}
	} else {
		// 并行
		b.SetParallelism(bc.concurrentGoroutines)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = NextInt()
			}
		})
	}
}

func BenchmarkZid_Single_4_18(b *testing.B)      { runBenchmark(b, benchCases[0]) }
func BenchmarkZid_Single_6_16(b *testing.B)      { runBenchmark(b, benchCases[1]) }
func BenchmarkZid_Single_8_14(b *testing.B)      { runBenchmark(b, benchCases[2]) }
func BenchmarkZid_Single_f_22(b *testing.B)      { runBenchmark(b, benchCases[3]) }
func BenchmarkZid_Shard_8_14_10g(b *testing.B)   { runBenchmark(b, benchCases[4]) }
func BenchmarkZid_Shard_10_12_50g(b *testing.B)  { runBenchmark(b, benchCases[5]) }
func BenchmarkZid_Shard_12_10_100g(b *testing.B) { runBenchmark(b, benchCases[6]) }
func BenchmarkZid_Shard_16_6_100g(b *testing.B)  { runBenchmark(b, benchCases[7]) }
