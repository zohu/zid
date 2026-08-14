package zid

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestBase62RoundTrip(t *testing.T) {
	for _, id := range []int64{0, 1, 61, 62, 63, 123456789, math.MaxInt64} {
		encoded := toBase62(id)
		decoded, err := fromBase62(encoded)
		if err != nil {
			t.Fatalf("fromBase62(%q): %v", encoded, err)
		}
		if decoded != id {
			t.Fatalf("round trip = %d, want %d", decoded, id)
		}
	}
}

func TestBase62RejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"", "-1", "_", "!", "abc🙂", strings.Repeat("Z", 12)} {
		if _, err := fromBase62(value); err == nil {
			t.Fatalf("fromBase62(%q) unexpectedly succeeded", value)
		}
	}
}

func TestParseHelpers(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		parse func(string) (int64, error)
		want  int64
	}{
		{"hex", "7fffffffffffffff", ParseHex, math.MaxInt64},
		{"base36", strconv.FormatInt(123456789, 36), ParseBase36, 123456789},
		{"base62", toBase62(123456789), ParseBase62, 123456789},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.parse(test.value)
			if err != nil || got != test.want {
				t.Fatalf("parse(%q) = %d, %v; want %d", test.value, got, err, test.want)
			}
		})
	}

	for _, value := range []string{"", "-1", "xyz"} {
		if _, err := ParseHex(value); err == nil {
			t.Fatalf("ParseHex(%q) unexpectedly succeeded", value)
		}
	}
}

func TestEncodedExtraction(t *testing.T) {
	id := NextID()
	hex := strconv.FormatInt(id, 16)
	base36 := strconv.FormatInt(id, 36)
	base62 := toBase62(id)

	if got, err := ExtractTimeHex(hex); err != nil || !got.Equal(ExtractTime(id)) {
		t.Fatalf("ExtractTimeHex() = %s, %v", got, err)
	}
	if got, err := ExtractTimeBase36(base36); err != nil || !got.Equal(ExtractTime(id)) {
		t.Fatalf("ExtractTimeBase36() = %s, %v", got, err)
	}
	if got, err := ExtractTimeBase62(base62); err != nil || !got.Equal(ExtractTime(id)) {
		t.Fatalf("ExtractTimeBase62() = %s, %v", got, err)
	}
	if got, err := ExtractWorkerIDHex(hex); err != nil || got != ExtractWorkerID(id) {
		t.Fatalf("ExtractWorkerIDHex() = %d, %v", got, err)
	}
	if got, err := ExtractWorkerIDBase36(base36); err != nil || got != ExtractWorkerID(id) {
		t.Fatalf("ExtractWorkerIDBase36() = %d, %v", got, err)
	}
	if got, err := ExtractWorkerIDBase62(base62); err != nil || got != ExtractWorkerID(id) {
		t.Fatalf("ExtractWorkerIDBase62() = %d, %v", got, err)
	}
}

func FuzzBase62RoundTrip(f *testing.F) {
	for _, seed := range []int64{0, 1, 61, 62, 1_000_000, math.MaxInt64} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, id int64) {
		if id < 0 {
			t.Skip()
		}
		decoded, err := fromBase62(toBase62(id))
		if err != nil || decoded != id {
			t.Fatalf("round trip = %d, %v; want %d", decoded, err, id)
		}
	})
}
