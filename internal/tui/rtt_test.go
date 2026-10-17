package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const ms = time.Millisecond

// wifi is a minute of round trips over Wi-Fi, a VPN and a jump host, as
// in #43: about 31 ms, but one in ten takes 100 ms or more.
var wifi = []time.Duration{28 * ms, 31 * ms, 30 * ms, 100 * ms, 33 * ms, 29 * ms, 35 * ms, 31 * ms, 212 * ms, 30 * ms}

func window(at time.Time, ds ...time.Duration) rttWindow {
	var w rttWindow
	for i, d := range ds {
		w.add(at.Add(time.Duration(i)*time.Second), d)
	}
	return w
}

func TestRTTSpread(t *testing.T) {
	end := func(n int) time.Time { return now.Add(time.Duration(n-1) * time.Second) }
	steady := []time.Duration{9 * ms, 9 * ms, 9 * ms, 9 * ms, 9 * ms, 9 * ms, 9 * ms, 9 * ms, 9 * ms, 9 * ms, 9 * ms}
	// late is a cold first ping, then a steady link.
	late := []time.Duration{80 * ms, 30 * ms, 30 * ms, 30 * ms, 30 * ms, 30 * ms, 30 * ms, 30 * ms, 30 * ms, 30 * ms}
	for _, tc := range []struct {
		name      string
		ds        []time.Duration
		mid, slow time.Duration
		text      string
		uneven    bool
	}{
		{"none", nil, 0, 0, "", false},
		{"too few for a spread", wifi[:9], 31 * ms, 212 * ms, "31ms", false},
		{"one late ping, too few", late[:5], 30 * ms, 80 * ms, "30ms", false},
		{"one late ping in ten", late, 30 * ms, 30 * ms, "30ms", false},
		{"uneven", wifi, 31 * ms, 100 * ms, "31/100ms", true},
		{"steady", steady, 9 * ms, 9 * ms, "9ms", false},
		{"a little uneven", []time.Duration{20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 69 * ms, 69 * ms}, 20 * ms, 69 * ms, "20/69ms", false},
		{"just uneven", []time.Duration{20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 20 * ms, 70 * ms, 70 * ms}, 20 * ms, 70 * ms, "20/70ms", true},
		{"two units", []time.Duration{310 * ms, 300 * ms, 320 * ms, 305 * ms, 315 * ms, 310 * ms, 300 * ms, 320 * ms, 1200 * ms, 1300 * ms}, 310 * ms, 1200 * ms, "310ms/1.2s", true},
		{"seconds", []time.Duration{1200 * ms, 1100 * ms, 1250 * ms, 1150 * ms, 1200 * ms, 1100 * ms, 1250 * ms, 1150 * ms, 3400 * ms, 3600 * ms}, 1200 * ms, 3400 * ms, "1.2/3.4s", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := window(now, tc.ds...).spread(end(len(tc.ds)))
			if s.mid != tc.mid || s.slow != tc.slow || s.n != len(tc.ds) {
				t.Errorf("spread = %+v, want median %v and 90th percentile %v of %d", s, tc.mid, tc.slow, len(tc.ds))
			}
			if got := s.text(); got != tc.text {
				t.Errorf("text = %q, want %q", got, tc.text)
			}
			if s.uneven() != tc.uneven {
				t.Errorf("uneven = %v, want %v", s.uneven(), tc.uneven)
			}
		})
	}
}

// A window forgets round trips older than a minute, and keeps no more
// than maxRTTs, however fast the pings come.
func TestRTTWindowForgets(t *testing.T) {
	w := window(now, 500*ms, 500*ms, 500*ms) // a slow start
	w.add(now.Add(70*time.Second), 9*ms)
	if len(w) != 1 || w[0].d != 9*ms {
		t.Fatalf("window = %v, want the newest round trip alone", w)
	}
	w = window(now, 9*ms, 9*ms, 9*ms, 300*ms)
	if s := w.spread(now.Add(63 * time.Second)); s.n != 1 || s.mid != 300*ms {
		t.Fatalf("a minute later the spread is %+v, want the last round trip alone", s)
	}
	w = nil
	for i := range 3 * maxRTTs {
		w.add(now.Add(time.Duration(i)*time.Millisecond), time.Duration(i)*ms)
	}
	if len(w) != maxRTTs || w[0].d != time.Duration(2*maxRTTs)*ms {
		t.Fatalf("kept %d round trips from %v, want the newest %d", len(w), w[0].d, maxRTTs)
	}
}

// The uneven part of a spread is in the warning colour, and the rest faint.
func TestRTTSegs(t *testing.T) {
	segs := window(now, wifi...).spread(now.Add(9 * time.Second)).segs()
	if len(segs) != 2 || segs[0].s != "31/" || segs[1].s != "100ms" || !segs[0].st.GetFaint() || segs[1].st.GetFaint() {
		t.Fatalf("segs = %+v, want a faint 31/ and 100ms as a warning", segs)
	}
	if segs := (rttSpread{}).segs(); segs != nil {
		t.Fatalf("no round trip drew %+v", segs)
	}
}

var (
	isRefresh = func(msg tea.Msg) bool { _, ok := msg.(refreshTickMsg); return ok }
	isLatency = func(msg tea.Msg) bool { _, ok := msg.(latencyTickMsg); return ok }
)

// refreshes lets n refreshes of the lists pass, each with its pings.
func refreshes(hs *harness, n int) {
	for range n {
		hs.clock = hs.clock.Add(refreshEvery)
		hs.fire(isRefresh)
	}
}

// unevenLab is the lab a minute after the launcher opened, with an uneven
// link to atlas-dev and a steadier one to gcp-dev.
func unevenLab(t *testing.T, w, h int) *harness {
	t.Helper()
	be := labBackend()
	be.state["atlas-dev"].rtts = wifi
	be.state["gcp-dev"].rtts = []time.Duration{41 * ms, 43 * ms, 40 * ms, 47 * ms, 42 * ms}
	hs := newHarness(t, be, w, h)
	hs.m.motion = false
	refreshes(hs, 30)
	hs.fire(isLatency)
	return hs
}

// header is the facts at the right of the header.
func header(hs *harness) string {
	line := strings.Split(plainScreen(hs.m), "\n")[headerY]
	_, facts, _ := strings.Cut(line, "atlas-dev")
	return strings.TrimSpace(facts)
}

// The launcher pings each online host with each refresh. The numbers it
// shows change only with the latency tick, every 10 s, as the one ping
// did before. A host the launcher no longer shows gets no more pings.
func TestRoundTripsShowEvery10s(t *testing.T) {
	be := labBackend()
	be.state["atlas-dev"].rtts = wifi
	hs := newHarness(t, be, 80, 24)
	hs.m.motion = false
	hs.key("enter") // atlas-dev's sessions
	for _, step := range []struct {
		refreshes, ticks int
		want, why        string
	}{
		{0, 0, "28ms", "the first round trip shows at once"},
		{4, 0, "28ms", "a ping changes nothing before the latency tick"},
		{0, 1, "31ms", "five round trips show their median alone"},
		{5, 0, "31ms", "a ping changes nothing before the latency tick"},
		{0, 1, "31/100ms", "ten round trips show their spread"},
	} {
		refreshes(hs, step.refreshes)
		for range step.ticks {
			hs.fire(isLatency)
		}
		if got, want := header(hs), step.want+" · 4 sessions · 1 needs you"; got != want {
			t.Fatalf("header = %q, want %q: %s", got, want, step.why)
		}
	}
	pings := func(host string) int {
		be.mu.Lock()
		defer be.mu.Unlock()
		return be.state[host].pings
	}
	if pings("atlas-dev") != 10 || pings("gcp-dev") != 10 || pings("aws-gpu") != 0 {
		t.Fatalf("pings: atlas-dev %d, gcp-dev %d, aws-gpu %d; want one at the dial and one with each refresh, and none to a host offline",
			pings("atlas-dev"), pings("gcp-dev"), pings("aws-gpu"))
	}
	hs.do(hs.m.removeHost("gcp-dev"))
	refreshes(hs, 2)
	if pings("gcp-dev") != 10 || pings("atlas-dev") != 12 {
		t.Fatalf("pings: gcp-dev %d after it went, atlas-dev %d", pings("gcp-dev"), pings("atlas-dev"))
	}
}

// A reconnect, as after a change of network, starts the round trips
// again: the first one shows at once, and the old ones no longer count.
func TestReconnectStartsRoundTripsAgain(t *testing.T) {
	hs := unevenLab(t, 80, 24)
	h := hs.m.hostByName("atlas-dev")
	if got := ansi.Strip(hs.m.hostState(h, 80)); got != "31/100ms · 4 sessions · 1 needs you" {
		t.Fatalf("before: %q", got)
	}
	hs.be.mu.Lock()
	hs.be.state["atlas-dev"].rtts = nil // a steady 9 ms from now on
	hs.be.mu.Unlock()
	hs.key("i", "r") // reconnect, from the host's details
	if got := ansi.Strip(hs.m.hostState(h, 80)); got != "9ms · 4 sessions · 1 needs you" {
		t.Fatalf("after the reconnect: %q, want the new round trip alone", got)
	}
	refreshes(hs, 9)
	hs.fire(isLatency)
	if got := ansi.Strip(hs.m.hostState(h, 80)); got != "9ms · 4 sessions · 1 needs you" {
		t.Fatalf("ten round trips after the reconnect: %q, want no old round trip in them", got)
	}
}

// When the header's facts do not fit, the session count goes first, then
// the 90th percentile.
func TestRoundTripFacts(t *testing.T) {
	hs := unevenLab(t, 80, 24)
	h := hs.m.hostByName("atlas-dev")
	for w, want := range map[int]string{
		80: "31/100ms · 4 sessions · 1 needs you",
		25: "31/100ms · 1 needs you",
		20: "31ms · 1 needs you",
		12: "1 needs you",
		5:  "",
	} {
		if got := ansi.Strip(hs.m.hostState(h, w)); got != want {
			t.Errorf("in %d cells: %q, want %q", w, got, want)
		}
	}
}
