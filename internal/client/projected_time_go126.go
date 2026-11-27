//go:build go1.26 && !go1.27

package client

import (
	"runtime"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// Numeric-zone framing/ranges here are pinned to Go1.26 format.go's
// stdISO8601ColonTZ/getnum branch. Its >24/>60 fallback is intentional.
// The original date, clock and fallback grammar still delegate to Go.
func parseProjectedTime(raw []byte) (projectedTime, error) {
	// Go build tags select a minor line, not a patch release. Only the exact
	// source-verified SDK uses this experimental numeric-zone projection.
	if runtime.Version() != "go1.26.11" {
		return parseProjectedTimeNative(raw)
	}
	return parseProjectedTimeGo12611(raw)
}

func parseProjectedTimeGo12611(raw []byte) (projectedTime, error) {
	var out projectedTime
	if string(raw) == "null" {
		return out, nil
	}
	if len(raw) < 3 || len(raw) > 64 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return out, api.ErrJSONProjection
	}
	end := len(raw) - 1
	var utc [64]byte
	start := end - 1
	if raw[end-1] != 'Z' {
		start = end - 6
		if start < 1 {
			return out, api.ErrJSONProjection
		}
		zone := raw[start:end]
		if zone[0] != '+' && zone[0] != '-' || zone[3] != ':' {
			return out, api.ErrJSONProjection
		}
		for _, i := range []int{1, 2, 4, 5} {
			if zone[i] < '0' || zone[i] > '9' {
				return out, api.ErrJSONProjection
			}
		}
		hour := int(zone[1]-'0')*10 + int(zone[2]-'0')
		minute := int(zone[4]-'0')*10 + int(zone[5]-'0')
		if hour > 24 || minute > 60 {
			return out, api.ErrJSONProjection
		}
		out.offset = (hour*60 + minute) * 60
		if zone[0] == '-' {
			out.offset = -out.offset
		}
	}
	n := copy(utc[:], raw[:start])
	// Canonicalise only the two Go1.26 accepted fallback spellings. This
	// reaches Go's UTC fast path. Allocation behaviour needs measurement.
	if n > 13 && utc[12] >= '0' && utc[12] <= '9' && utc[13] == ':' {
		if n+3 > len(utc) {
			return out, api.ErrJSONProjection
		}
		copy(utc[13:n+1], utc[12:n])
		utc[12] = '0'
		n++
	}
	if n > 20 && utc[20] == ',' {
		utc[20] = '.'
	}
	if n+2 > len(utc) {
		return out, api.ErrJSONProjection
	}
	utc[n], utc[n+1] = 'Z', '"'
	if err := out.local.UnmarshalJSON(utc[:n+2]); err != nil {
		return out, api.ErrJSONProjection
	}
	out.instant = out.local.Add(-time.Duration(out.offset) * time.Second)
	return out, nil
}
