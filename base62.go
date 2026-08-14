package zid

import (
	"errors"
	"fmt"
	"math"
)

// base62Chars follows ASCII/Unicode code-point order so equal-width encoded
// values preserve their numeric order under bytewise comparison.
const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var base62Map [256]int8

func init() {
	for index := range base62Map {
		base62Map[index] = -1
	}
	for index := range len(base62Chars) {
		base62Map[base62Chars[index]] = int8(index)
	}
}

func fromBase62(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("zid: empty base-62 ID")
	}

	var result int64
	for _, character := range value {
		if character >= 256 || base62Map[character] < 0 {
			return 0, fmt.Errorf("zid: invalid base-62 character %q", character)
		}
		digit := int64(base62Map[character])
		if result > (math.MaxInt64-digit)/62 {
			return 0, errors.New("zid: base-62 ID exceeds int64")
		}
		result = result*62 + digit
	}
	return result, nil
}

func toBase62(id int64) string {
	if id == 0 {
		return "0"
	}
	if id < 0 {
		panic("zid: ID must not be negative")
	}

	var buffer [11]byte
	position := len(buffer)
	for id > 0 {
		position--
		buffer[position] = base62Chars[id%62]
		id /= 62
	}
	return string(buffer[position:])
}
