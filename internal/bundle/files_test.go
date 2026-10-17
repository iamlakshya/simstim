//go:build !windows

package bundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// newTestBuilder is a builder that writes into a new archive.
func newTestBuilder(t *testing.T, o Options) *builder {
	t.Helper()
	a, err := newArchive(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.abort)
	return &builder{o: o, res: &Result{}, a: a, s: NewScrubber(Values{})}
}

// A name that the host gives must be a plain path under host/, or the
// bundle leaves the file out without asking for it. The host's notes lose
// their control codes.
func TestTheClientTrustsNoNameFromTheHost(t *testing.T) {
	h := client.Host{Name: "box"}
	b := newTestBuilder(t, Options{Host: &h})
	bad := []string{"../../.zshrc", "/etc/x", "laptop/x", "host/\x1b]52;c;eA==\x07x", "host/../../x"}
	for _, p := range bad {
		b.col.Files = append(b.col.Files, client.DebugFileInfo{Path: p, Size: 1})
	}
	b.col.Notes = []string{"note \x1b]52;c;eA==\x07 end"}
	// b.conn is nil: a name that passed would make the bundle ask for it.
	if err := b.hostFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.res.Left) != len(bad) {
		t.Fatalf("left out %+v", b.res.Left)
	}
	for _, l := range b.res.Left {
		if strings.ContainsAny(l.Name, "\x1b\x07") {
			t.Errorf("a name with control codes: %q", l.Name)
		}
	}
	if len(b.res.Notes) != 1 || strings.ContainsAny(b.res.Notes[0], "\x1b\x07") {
		t.Errorf("notes %q", b.res.Notes)
	}
	for _, f := range b.a.files {
		if f.Name != "host/doctor.json" {
			t.Errorf("the archive took %s", f.Name)
		}
	}
}

// replaced.txt goes through the last check too. When what it says of a
// file left out still holds a private value, it keeps only the counts.
func TestReplacedTxtIsCheckedToo(t *testing.T) {
	s := NewScrubber(Values{Sessions: []string{"nebula"}, Hosts: []string{"zorblax"}})
	r := &Result{Counts: []Count{{Kind: KindHost, N: 2}}, Left: []Left{{Name: "laptop/x", Why: "cannot read zorblax-x"}}}
	if got := string(replacedFile(r, s)); !strings.Contains(got, "<host-1>-x") || !strings.Contains(got, "host names") {
		t.Errorf("replaced.txt:\n%s", got)
	}
	r.Left[0].Why = "cannot read nebula_x"
	got := string(replacedFile(r, s))
	if strings.Contains(got, "nebula") || !strings.Contains(got, "left the list out") || !strings.Contains(got, "host names") {
		t.Errorf("replaced.txt:\n%s", got)
	}
}

// On the laptop, the bundle takes from the debug folder only the files
// whose names the debug mode gives them, and none that is a link: a link
// in place of a debug file must not ship what it points to.
func TestTheLaptopsLinksStayOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := client.DebugDir()
	if err := diag.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(home, "id_ed25519")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(key, "PRIVATE KEY")
	write(filepath.Join(dir, "client-20261016-000000-1.jsonl"), `{"ev":"start"}`+"\n")
	write(filepath.Join(dir, "notes.txt"), "PRIVATE notes")
	if err := os.Symlink(key, filepath.Join(dir, "client-20261016-000000-2.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(key, filepath.Join(dir, "client-20261016-000000-3.jsonl")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "elsewhere.jsonl")
	if err := os.Symlink(key, outside); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, diag.OutsideList), outside+"\n")
	b := newTestBuilder(t, Options{})
	if err := b.laptopFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	var took []string
	for _, f := range b.a.files {
		took = append(took, f.Name)
	}
	if strings.Join(took, " ") != "laptop/doctor.txt laptop/client-20261016-000000-1.jsonl" {
		t.Errorf("the bundle took %v", took)
	}
	left := map[string]bool{}
	for _, l := range b.res.Left {
		left[l.Name] = true
	}
	for _, n := range []string{"laptop/client-20261016-000000-2.jsonl", "laptop/client-20261016-000000-3.jsonl", "laptop/outside/1.jsonl"} {
		if !left[n] {
			t.Errorf("%s is not among the files left out: %+v", n, b.res.Left)
		}
	}
}
