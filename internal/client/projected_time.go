package client

import (
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// projectedTime preserves the instant and original local clock/offset.
// Only selected records construct a fixed zone for final serialization.
type projectedTime struct {
	instant time.Time
	local   time.Time
	offset  int
}

func (t projectedTime) marshalable() bool {
	return t.local.Year() >= 0 && t.local.Year() <= 9999 && t.offset > -24*60*60 && t.offset < 24*60*60
}
func (t projectedTime) marshalJSON() ([]byte, error) {
	return t.instant.In(time.FixedZone("", t.offset)).MarshalJSON()
}

// compactProjectedTime keeps the historical Go nanosecond precision while
// walking arbitrary fractional runs. The source bytes are never rewritten.
// All accepted RFC3339 core spellings fit this temporary validation buffer.
func compactProjectedTime(p jsonValueProjection, span api.JSONSpan, compact *[64]byte) (int, error) {
	n := 0
	fraction, digits := false, 0
	err := p.WalkEncoded(span, func(piece []byte) error {
		for _, b := range piece {
			if fraction && b >= '0' && b <= '9' {
				digits = min(digits+1, 10)
				if digits > 9 {
					continue
				}
			} else {
				fraction = false
				if (b == '.' || b == ',') && (n == 19 || n == 20) {
					fraction, digits = true, 0
				}
			}
			if n == len(compact) {
				return api.ErrJSONProjection
			}
			compact[n], n = b, n+1
		}
		return nil
	})
	return n, err
}
func readProjectedTime(p jsonValueProjection, span api.JSONSpan) (projectedTime, error) {
	var compact [64]byte
	n, err := compactProjectedTime(p, span, &compact)
	if err != nil {
		return projectedTime{}, err
	}
	return parseProjectedTime(compact[:n])
}

// The fallback delegates native Go acceptance without a zone grammar fork.
// It preserves the local clock even when its UTC instant is Go zero.
func parseProjectedTimeNative(raw []byte) (projectedTime, error) {
	var out projectedTime
	var value time.Time
	if err := value.UnmarshalJSON(raw); err != nil {
		return out, api.ErrJSONProjection
	}
	_, out.offset = value.Zone()
	y, m, d := value.Date()
	h, minute, second := value.Clock()
	out.local = time.Date(y, m, d, h, minute, second, value.Nanosecond(), time.UTC)
	out.instant = value.UTC()
	return out, nil
}
