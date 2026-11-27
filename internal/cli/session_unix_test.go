//go:build unix

package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// Through a real host: an agent that exits with a code, and one that a
// signal kills. ls says why each ended, and attach says what to try, while
// their holders still answer.
func TestLsSaysWhyAnAgentEnded(t *testing.T) {
	newTestHost(t)
	crash := newSession(t, "crash", "/bin/sh", "-c", "exit 3")
	shot := newSession(t, "shot", "/bin/sh", "-c", "kill -KILL $$")
	want := []*regexp.Regexp{
		regexp.MustCompile(crash + `\s+crash\s+.*errored \(exit code 3\)`),
		regexp.MustCompile(shot + `\s+shot\s+.*errored \(killed\)`),
	}
	hosttest.WaitFor(t, 10*time.Second, "ls to say why both agents ended", func() bool {
		out := ok(t, "ls")
		return want[0].MatchString(out) && want[1].MatchString(out)
	})
	if out := ok(t, "show", "box/crash"); !strings.Contains(out, "errored (exit code 3, from ") {
		t.Errorf("show box/crash:\n%s", out)
	}
}
