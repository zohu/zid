package zid

import (
	"errors"
	"fmt"
)

const base62Chars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var base62Map [256]int8

func init() {
	for i := 0; i < len(base62Chars); i++ {
		c := base62Chars[i]
		base62Map[c] = int8(i)
	}
}

func fromBase62(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty string")
	}

	var result int64 = 0
	const maxInt64 = 1<<63 - 1 // 9223372036854775807

	for _, c := range s {
		if c >= 256 {
			return 0, fmt.Errorf("invalid character: %c", c)
		}
		val := base62Map[c]
		if val < 0 {
			return 0, fmt.Errorf("invalid base62 character: %c", c)
		}

		// 检查溢出：result * 62 + val > maxInt64 ?
		if result > (maxInt64-int64(val))/62 {
			return 0, errors.New("value overflow: exceeds int64 max")
		}

		result = result*62 + int64(val)
	}

	return result, nil
}

func toBase62(id int64) string {
	if id == 0 {
		return "0"
	}
	buf := make([]byte, 0, 11)
	for id > 0 {
		buf = append(buf, base62Chars[id%62])
		id /= 62
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
