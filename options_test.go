package zid

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDefaultOptions(t *testing.T) {
	wantBaseTime := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	options := DefaultOptions()
	if options.BaseTime != wantBaseTime {
		t.Fatalf("BaseTime = %d, want %d", options.BaseTime, wantBaseTime)
	}
	if options.WorkerIDBits != 4 || options.SequenceBits != 18 || options.ShardBits != 0 {
		t.Fatalf("unexpected defaults: %+v", options)
	}
}

func TestNormalizeOptions(t *testing.T) {
	now := DefaultBaseTime + 10_000
	tests := []struct {
		name    string
		options Options
		wantErr string
	}{
		{name: "zero value"},
		{name: "worker disabled", options: Options{DisableWorkerID: true, SequenceBits: 22}},
		{name: "disabled worker conflict", options: Options{DisableWorkerID: true, WorkerIDBits: 1, SequenceBits: 21}, wantErr: "cannot be combined"},
		{name: "custom layout", options: Options{WorkerID: 7, WorkerIDBits: 3, ShardBits: 4, SequenceBits: 15}},
		{name: "future epoch", options: Options{BaseTime: now + 1}, wantErr: "BaseTime"},
		{name: "worker bits", options: Options{WorkerIDBits: 20}, wantErr: "WorkerIDBits"},
		{name: "shard bits", options: Options{ShardBits: 17}, wantErr: "ShardBits"},
		{name: "sequence too small", options: Options{SequenceBits: 2}, wantErr: "SequenceBits"},
		{name: "sequence too large", options: Options{SequenceBits: 23}, wantErr: "SequenceBits"},
		{name: "layout too large", options: Options{WorkerIDBits: 10, ShardBits: 1, SequenceBits: 12}, wantErr: "must be <="},
		{name: "worker out of range", options: Options{WorkerID: 16, WorkerIDBits: 4, SequenceBits: 18}, wantErr: "WorkerID must"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := normalizeOptions(test.options, now)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("normalizeOptions() error = %v", err)
				}
				if cfg.shardCount == 0 {
					t.Fatal("shardCount must not be zero")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("normalizeOptions() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestNormalizeOptionsRejectsExpiredLayout(t *testing.T) {
	_, err := normalizeOptions(Options{
		BaseTime:     1,
		WorkerIDBits: 19,
		SequenceBits: 3,
	}, math.MaxInt64)
	if err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("error = %v, want exhausted layout", err)
	}
}

func TestBitMask(t *testing.T) {
	for _, test := range []struct {
		bits uint8
		want uint32
	}{{0, 0}, {1, 1}, {4, 15}, {22, 4_194_303}} {
		if got := bitMask(test.bits); got != test.want {
			t.Fatalf("bitMask(%d) = %d, want %d", test.bits, got, test.want)
		}
	}
}
