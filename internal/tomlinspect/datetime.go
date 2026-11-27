package tomlinspect

import (
	"errors"
	"time"
)

// ErrDatetime is a native date refusal without retaining the source value.
var ErrDatetime = errors.New("invalid pinned TOML native date")

// The pin's internal/tz.go samples the current local offset once, then uses
// three named fixed zones for local TOML values. These are not date-dependent
// time.Local zones. This extraction preserves that initialisation behaviour.
// See primary-toml-addendum/internal/tz.go and COPYING.
var (
	localOffset   = func() int { _, offset := time.Now().Zone(); return offset }()
	localDatetime = time.FixedZone("datetime-local", localOffset)
	localDate     = time.FixedZone("date-local", localOffset)
	localTime     = time.FixedZone("time-local", localOffset)
)

// dtTypes is the pinned parse.go order, including the TOML 1.1 minute forms.
var dtTypes = [...]struct {
	format string
	zone   *time.Location
}{
	{time.RFC3339Nano, time.Local},
	{"2006-01-02T15:04:05.999999999", localDatetime},
	{"2006-01-02", localDate},
	{"15:04:05.999999999", localTime},
	{"2006-01-02T15:04Z07:00", time.Local},
	{"2006-01-02T15:04", localDatetime},
	{"15:04", localTime},
}

// dateValue scans the complete borrowed token and calls the native parser
// with a bounded representation. This is not a new input-length cap:
// Go 1.26.11 time.parseRFC3339 and stdFracSecond9 scan all fractional digits;
// parseNanoseconds uses only the first nine, with no rounding. All seven
// layouts have at most 26 other bytes. Full digits and suffix are checked
// before that equivalence is used. The native parser still chooses its own
// RFC3339 fast/fallback path, local zone and offset acceptance.
func dateValue(s numberSpan) (time.Time, error) {
	var bounded [35]byte
	n, fraction := 0, -1
	for i := 0; i < s.len(); i++ {
		c := s.at(i)
		switch c {
		case 't', ' ':
			c = 'T'
		case 'z':
			c = 'Z'
		}
		if fraction >= 0 && c >= '0' && c <= '9' {
			fraction++
			if fraction > 9 {
				continue
			}
		} else if c == '.' && fraction == -1 {
			fraction = 0
		} else if fraction >= 0 {
			// A second period must not restart digit elision. Keeping it
			// lets the native parser reject the unchanged invalid suffix.
			fraction = -2
		}
		if n == len(bounded) {
			return time.Time{}, ErrDatetime
		}
		bounded[n] = c
		n++
	}
	value := string(bounded[:n])
	for _, layout := range dtTypes {
		if err := s.source.ctx.Err(); err != nil {
			return time.Time{}, err
		}
		parsed, err := time.ParseInLocation(layout.format, value, layout.zone)
		if err != nil {
			continue
		}
		if missingLeadingZero(value, layout.format) {
			return time.Time{}, ErrDatetime
		}
		return parsed, nil
	}
	return time.Time{}, ErrDatetime
}

// missingLeadingZero is the pinned parse.go post-parse separator check.
func missingLeadingZero(value, layout string) bool {
	for i := range len(layout) {
		c := layout[i]
		if c == '.' || c == 'Z' {
			return false
		}
		if (c < '0' || c > '9') && (i >= len(value) || value[i] != c) {
			return true
		}
	}
	return false
}
