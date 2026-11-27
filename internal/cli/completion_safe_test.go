package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestCompletionKeepsSessionReferencesExact(t *testing.T) {
	list := []client.Session{
		{ID: "aaaa2345", Name: " padded "},
		{ID: "bbbb2345", Name: "colour\x1b[31med"},
		{ID: "cccc2345", Name: "dddd2345"},
		{ID: "abcdefgh", Name: "legacy"},
		{ID: "eeee2345", Name: "abcdefgh"},
		{ID: "ffff2345", Name: "duplicate"},
		{ID: "gggg2345", Name: "duplicate"},
		{ID: "hhhh2345", Name: "fix auth"},
		{ID: "bad\t-id", Name: "invalid tab"},
		{ID: "bad\x1b[31m-id", Name: "invalid escape"},
		{ID: " bad-id", Name: "invalid space"},
		{ID: "bad/id", Name: "invalid slash"},
		{ID: "", Name: "missing ID"},
	}
	items := appendHostSessions(nil, "box", list, time.Time{}, time.Now(), "")
	cmd := Root()
	got := make([]string, len(items))
	for i, item := range items {
		got[i], _, _ = strings.Cut(item, "\t")
		ref := strings.TrimPrefix(got[i], "box/")
		s, err := findIn(cmd, list, ref, "box")
		if err != nil {
			t.Errorf("completion %q cannot resolve: %v", got[i], err)
			continue
		}
		if s.ID == "" || cleanCompValue(s.ID) != s.ID || strings.TrimSpace(s.ID) != s.ID || strings.Contains(s.ID, "/") {
			t.Errorf("completion %q resolved an invalid ID %q", got[i], s.ID)
		}
	}
	want := []string{"box/aaaa2345", "box/bbbb2345", "box/cccc2345", "box/legacy", "box/eeee2345", "box/ffff2345", "box/gggg2345", "box/fix auth"}
	if !slices.Equal(got, want) {
		t.Errorf("session addresses = %q, want %q", got, want)
	}
}

func TestCompletionRejectsIDsChangedByCleaning(t *testing.T) {
	for _, id := range []string{"abc\n2345", " abc2345", "abc2345 ", "abc/2345", "abc\x1b[31m2345", strings.Repeat("a", api.SessionNameMax+1)} {
		items := appendHostSessions(nil, "box", []client.Session{{ID: id, Name: "safe name"}}, time.Time{}, time.Now(), "")
		if len(items) != 0 {
			t.Errorf("ID %q produced %q", id, items)
		}
	}
}

func TestAgentListCompletionTreatsNoneAsExclusive(t *testing.T) {
	for _, value := range []string{"none,", "none,co", "claude,none,", "claude,none,co"} {
		items, directive := completeAgentList(nil, nil, value)
		if len(items) != 0 || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("%q = %q, %d, want no values and NoFileComp", value, items, directive)
		}
	}
	items, directive := completeAgentList(nil, nil, "no")
	if !slices.Equal(items, []string{"none"}) || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("none = %q, %d, want one complete value with a trailing space", items, directive)
	}
	items, directive = completeAgentList(nil, nil, "claude,co")
	if len(items) == 0 || directive != cobra.ShellCompDirectiveNoFileComp|cobra.ShellCompDirectiveNoSpace {
		t.Errorf("normal comma list = %q, %d", items, directive)
	}
}

func TestCompletionDescriptionStripsCompleteEscapes(t *testing.T) {
	for _, text := range []string{"one\x1b(0two", "one\x1b#8two", "one\x1b(\x20Btwo", "one\u009b31mtwo", "one\x1b]52;c;payload\x07two"} {
		if got := cleanCompDesc(text); got != "onetwo" {
			t.Errorf("description %q = %q, want onetwo", text, got)
		}
	}
	for _, text := range []string{"  one   two  ", "  one\t\n two  ", "  one\u2003 two  "} {
		if got := cleanCompDesc(text); got != "one two" {
			t.Errorf("description %q = %q, want one two", text, got)
		}
	}
}
