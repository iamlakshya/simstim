package api

import (
	"strings"
	"testing"
)

func TestParseMemoryMax(t *testing.T) {
	for in, want := range map[string]int64{
		"":         0,
		"auto":     0,
		" Auto ":   0,
		"256M":     256 << 20,
		"512m":     512 << 20,
		"4G":       4 << 30,
		"4GiB":     4 << 30,
		"4Gi":      4 << 30,
		"4gi":      4 << 30,
		"4GIB":     4 << 30,
		"512Mi":    512 << 20,
		"1Ti":      1 << 40,
		"4gb":      4 << 30,
		"4 GiB":    4 << 30,
		"1.5G":     1536 << 20,
		"262144K":  256 << 20,
		"1T":       1 << 40,
		"0.25G":    256 << 20,
		"1536MiB":  1536 << 20,
		"1048576k": 1 << 30,
	} {
		if got, err := ParseMemoryMax(in); err != nil || got != want {
			t.Errorf("ParseMemoryMax(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	for in, why := range map[string]string{
		"4096":      "has no unit",
		"4":         "has no unit",
		"1.5":       "has no unit",
		"infinity":  "auto already lets a session use all",
		"Max":       "auto already lets a session use all",
		"inf":       "auto already lets a session use all",
		"4Bi":       "is not a size",
		"4iB":       "is not a size",
		"lots":      "is not a size",
		"4X":        "is not a size",
		"-1G":       "is not a size",
		"4 G B":     "is not a size",
		"G":         "is not a size",
		"255M":      "below the smallest cap, 256M",
		"0G":        "below the smallest cap",
		"10000000T": "is too big",
	} {
		if got, err := ParseMemoryMax(in); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("ParseMemoryMax(%q) = %d, %v, want an error that it %s", in, got, err, why)
		}
	}
}
