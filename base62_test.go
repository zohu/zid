package zid

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestBase62Alphabet(t *testing.T) {
	tests := []struct {
		id      int64
		encoded string
	}{
		{0, "0"},
		{9, "9"},
		{10, "A"},
		{35, "Z"},
		{36, "a"},
		{61, "z"},
		{62, "10"},
	}

	for _, test := range tests {
		if got := toBase62(test.id); got != test.encoded {
			t.Errorf("toBase62(%d) = %q, want %q", test.id, got, test.encoded)
		}
		if got, err := fromBase62(test.encoded); err != nil || got != test.id {
			t.Errorf("fromBase62(%q) = %d, %v; want %d", test.encoded, got, err, test.id)
		}
	}
}

func TestBase62EqualWidthValuesSortNumerically(t *testing.T) {
	previous := toBase62(0)
	for id := int64(1); id < 62*62*62; id++ {
		encoded := toBase62(id)
		if len(previous) == len(encoded) && previous >= encoded {
			t.Fatalf("numeric order %d < %d encoded as %q >= %q", id-1, id, previous, encoded)
		}
		previous = encoded
	}
}

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
