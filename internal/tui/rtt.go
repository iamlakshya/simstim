package tui

import (
	"slices"
	"strings"
	"time"
)

// Round trips (docs/contributing/ux-spec.md 2.3). The launcher pings each
// host with each refresh of the lists, and keeps the round trips of the
// last minute. It shows their median and their 90th percentile, and marks
// an uneven link: one where some keys wait much longer than the median
// says. A ping comes every 2 s, but the numbers change only with the
// latency tick, every 10 s, so the pings repaint nothing on their own.

const (
	// rttSpan is how far back a host's round trips count.
	rttSpan = time.Minute
	// maxRTTs caps the round trips a host keeps, whatever the pings' rate.
	maxRTTs = 64
	// minSpread is how many round trips a window needs before it shows
	// its 90th percentile. With fewer, the 90th percentile is the slowest
	// round trip, so one late ping, such as the first on a cold
	// connection, would mark the link.
	minSpread = 10
	// unevenBy is how far the 90th percentile of an uneven link is above
	// its median: one ping in ten takes at least this much longer.
	unevenBy = 50 * time.Millisecond
)

// rttSample is one round trip, and when it ended.
type rttSample struct {
	at time.Time
	d  time.Duration
}

// rttWindow is a host's recent round trips, oldest first.
type rttWindow []rttSample

// add keeps a round trip that ended at at, and forgets those that no
// longer count.
func (w *rttWindow) add(at time.Time, d time.Duration) {
	s := append(*w, rttSample{at, d})
	drop := max(len(s)-maxRTTs, 0)
	for drop < len(s) && at.Sub(s[drop].at) > rttSpan {
		drop++
	}
	*w = s[drop:]
}

// rttSpread is the spread of a window: the median and the 90th
// percentile of its n round trips.
type rttSpread struct {
	mid, slow time.Duration
	n         int
}

// spread is the spread of the round trips in the last rttSpan before now.
func (w rttWindow) spread(now time.Time) rttSpread {
	ds := make([]time.Duration, 0, len(w))
	for _, s := range w {
		if now.Sub(s.at) <= rttSpan {
			ds = append(ds, s.d)
		}
	}
	if len(ds) == 0 {
		return rttSpread{}
	}
	slices.Sort(ds)
	return rttSpread{mid: percentile(ds, 50), slow: percentile(ds, 90), n: len(ds)}
}

// percentile is the nearest-rank percentile p of sorted: the smallest
// value that at least p percent of the values are no larger than.
func percentile(sorted []time.Duration, p int) time.Duration {
	i := (len(sorted)*p+99)/100 - 1
	return sorted[max(i, 0)]
}

// uneven reports a link whose 90th percentile is far above its median.
func (s rttSpread) uneven() bool { return s.n >= minSpread && s.slow-s.mid >= unevenBy }

// parts writes the spread as the launcher shows it: the median, then the
// 90th percentile after a slash, with one unit when both share it:
// "31/100ms", "310ms/1.2s". The median shows alone, with its unit, until
// the window has minSpread round trips, or when the two read the same.
// lead is the median and the slash, and slow is "" when it does not show.
func (s rttSpread) parts() (lead, slow string) {
	mid := fmtLatency(s.mid)
	if s.n < minSpread {
		return mid, ""
	}
	slow = fmtLatency(s.slow)
	if slow == mid {
		return mid, ""
	}
	if u := unit(mid); u == unit(slow) {
		mid = strings.TrimSuffix(mid, u)
	}
	return mid + "/", slow
}

// unit is the unit of a time that fmtLatency wrote.
func unit(s string) string {
	if strings.HasSuffix(s, "ms") {
		return "ms"
	}
	return "s"
}

// text is the spread as one plain string.
func (s rttSpread) text() string {
	lead, slow := s.parts()
	return lead + slow
}

// segs is the spread in faint, with the 90th percentile of an uneven link
// in the warning colour. Without colour, it still stands out from the faint
// text round it, as the count that needs you does.
func (s rttSpread) segs() []seg {
	lead, slow := s.parts()
	if lead == "" {
		return nil
	}
	out := []seg{{lead, sFaint}}
	if slow != "" {
		st := sFaint
		if s.uneven() {
			st = sWarning
		}
		out = append(out, seg{slow, st})
	}
	return out
}

// renderSegs draws segs as one string.
func renderSegs(segs []seg) string {
	var b strings.Builder
	for _, sg := range segs {
		b.WriteString(sg.st.Render(sg.s))
	}
	return b.String()
}
