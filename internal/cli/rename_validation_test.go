//go:build unix

package cli

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestRenameRequiresNameBeforeDialAndCachesTrimmedName(t *testing.T) {
	th := newTestHost(t)
	id := newSession(t, "before", "sleep", "60")
	for _, name := range []string{"", "  ", "\t\n"} {
		before := bridges(th.tr)
		text, code := failsAs(t, "rename", "box/"+id, name)
		if code != 2 || bridges(th.tr) != before || !strings.Contains(text, "a name is needed") || !strings.Contains(text, "Try:") {
			t.Fatalf("empty rename: code=%d connections=%d text=%q", code, bridges(th.tr)-before, text)
		}
	}
	okSaid(t, "rename", "box/"+id, "  after  ")
	list := client.LoadSessionCache().Sessions["box"]
	if len(list) != 1 || list[0].Name != "after" {
		t.Fatalf("rename cache = %+v", list)
	}
	if got := idOf(t, th, "after"); got != id {
		t.Fatalf("trimmed rename changed identity: %q", got)
	}
}
