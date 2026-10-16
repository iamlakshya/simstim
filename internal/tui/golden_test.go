package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/client"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// screen is what the terminal shows, without colours or styles.
func plainScreen(m *Model) string {
	lines := strings.Split(ansi.Strip(m.render()), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func golden(t *testing.T, name string, m *Model) {
	t.Helper()
	got := plainScreen(m)
	for i, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("%s: line %d is %d cells wide, more than %d", name, i+1, w, m.width)
		}
	}
	if n := strings.Count(got, "\n"); n != m.height {
		t.Errorf("%s: %d lines, want %d", name, n, m.height)
	}
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/tui -update to write it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s:\n--- got\n%s--- want\n%s", name, path, got, want)
	}
}

// home is the lab on the hosts screen, with atlas-dev selected.
func home(t *testing.T, w, h int) *harness {
	hs := newHarness(t, labBackend(), w, h)
	hs.m.motion = false
	return hs
}

// sessions is the lab on atlas-dev's sessions.
func sessions(t *testing.T, w, h int) *harness {
	hs := home(t, w, h)
	hs.key("enter")
	return hs
}

var sizes = []struct{ w, h int }{{60, 20}, {80, 24}, {120, 36}}

func TestGolden(t *testing.T) {
	views := []struct {
		name  string
		setup func(t *testing.T, w, h int) *Model
	}{
		{"first_run", func(t *testing.T, w, h int) *Model {
			return newHarness(t, newFakeBackend(), w, h).m
		}},
		{"hosts", func(t *testing.T, w, h int) *Model { return home(t, w, h).m }},
		{"sessions", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("down")
			return hs.m
		}},
		{"ended", func(t *testing.T, w, h int) *Model { return endedLab(t, w, h).m }},
		{"all_sessions", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.m.openHost(nil)
			return hs.m
		}},
		{"offline", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.key("down", "enter")
			return hs.m
		}},
		{"empty_host", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.be.state["gcp-dev"].sessions = nil
			hs.m.hostByName("gcp-dev").sessions = nil
			hs.key("down", "down", "enter")
			return hs.m
		}},
		{"new_session", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.key("n")
			hs.typeText("claude ~/src/a")
			return hs.m
		}},
		{"new_session_form", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.key("n")
			hs.typeText("codex ~/src/app auth")
			hs.key("ctrl+f")
			return hs.m
		}},
		{"search", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.key("/")
			hs.typeText("te")
			return hs.m
		}},
		{"new_session_error", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.be.state["atlas-dev"].newErr = &client.UserError{Msg: "aider is not installed on this host", Hint: "Install it on the host, then try again."}
			hs.key("n", "enter")
			return hs.m
		}},
		{"delete", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("down", "d")
			return hs.m
		}},
		{"rename", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("r", "ctrl+u")
			hs.typeText("auth-v2")
			return hs.m
		}},
		{"search_empty", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("/")
			hs.typeText("xyz")
			return hs.m
		}},
		{"palette", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("ctrl+o")
			hs.typeText("ses")
			return hs.m
		}},
		{"help", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("?")
			return hs.m
		}},
		{"preview", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("space")
			return hs.m
		}},
		{"details", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.key("i")
			return hs.m
		}},
		{"uneven_hosts", func(t *testing.T, w, h int) *Model { return unevenLab(t, w, h).m }},
		{"uneven_sessions", func(t *testing.T, w, h int) *Model {
			hs := unevenLab(t, w, h)
			hs.key("enter")
			return hs.m
		}},
		{"uneven_details", func(t *testing.T, w, h int) *Model {
			hs := unevenLab(t, w, h)
			hs.key("i")
			return hs.m
		}},
		{"notices", func(t *testing.T, w, h int) *Model {
			hs := sessions(t, w, h)
			hs.key("y")
			hs.m.toastErr(&client.UserError{Msg: "cannot reach aws-gpu: timed out", Hint: "ssh aws-gpu"})
			return hs.m
		}},
		{"quit", func(t *testing.T, w, h int) *Model {
			hs := home(t, w, h)
			hs.key("ctrl+c")
			return hs.m
		}},
		{"wizard_where", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			be.known = []string{"atlas-dev", "gcp-dev", "hetzner-1"}
			hs := newHarness(t, be, w, h)
			hs.key("a")
			hs.typeText("het")
			return hs.m
		}},
		{"wizard_test_failed", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			be.probeErr = &client.UserError{Msg: "cannot reach box: Permission denied (publickey).", Hint: "ssh box   (check that this works first)"}
			hs := newHarness(t, be, w, h)
			hs.key("a")
			hs.typeText("box")
			hs.key("enter")
			return hs.m
		}},
		{"wizard_install", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			be.probe = client.Platform{OS: "linux", Arch: "arm64"}
			hs := newHarness(t, be, w, h)
			hs.key("a")
			hs.typeText("pi@raspberry")
			hs.key("enter", "enter")
			return hs.m
		}},
		{"wizard_ready", func(t *testing.T, w, h int) *Model {
			be := newFakeBackend()
			be.probe = client.Platform{OS: "linux", Arch: "arm64"}
			hs := newHarness(t, be, w, h)
			hs.key("a")
			hs.typeText("pi@raspberry")
			hs.key("enter", "enter", "enter")
			return hs.m
		}},
	}
	for _, v := range views {
		for _, s := range sizes {
			name := fmt.Sprintf("%s_%dx%d", v.name, s.w, s.h)
			t.Run(name, func(t *testing.T) {
				golden(t, name, v.setup(t, s.w, s.h))
			})
		}
	}
}
