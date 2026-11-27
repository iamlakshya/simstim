package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// These model checks use the actual local placement resolver and writer.
// Connections, starts and terminal ownership remain the existing fake backend.
func newPlacementFixture(t *testing.T, saved string) *harness {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}
	t.Setenv(brand.Env("DEFAULT_HOST"), "")
	hs := home(t, 100, 36)
	if err := client.SaveHosts(t.Context(), hs.be.hosts); err != nil {
		t.Fatal(err)
	}
	for i, host := range hs.be.hosts {
		if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, strings.Repeat(string(rune('a'+i)), 32), ""); err != nil {
			t.Fatal(err)
		}
	}
	if saved != "" {
		if err := client.SetDefaultHost(t.Context(), saved); err != nil {
			t.Fatal(err)
		}
	}
	hs.be.resolveHost = client.ResolveHost
	return hs
}

func TestLauncherNewUsesCanonicalDefaultRatherThanTheHighlightedHost(t *testing.T) {
	for _, test := range []struct{ name, environment, want string }{
		{"saved", "", "gcp-dev"},
		{"environment", "atlas-dev", "atlas-dev"},
	} {
		t.Run(test.name, func(t *testing.T) {
			hs := newPlacementFixture(t, "gcp-dev")
			t.Setenv(brand.Env("DEFAULT_HOST"), test.environment)
			hs.m.hostSel = 1 // the highlighted aws-gpu is offline
			hs.m.mem.Last = &lastSession{Host: "aws-gpu", ID: "last2345"}
			hs.key("n")
			dialog, ok := hs.m.modal.(*newSession)
			if !ok || dialog.host != test.want || dialog.selector != "" || dialog.defaultHost != test.want || hs.m.prompt != nil {
				t.Fatalf("n ignored the canonical default: modal=%T prompt=%v", hs.m.modal, hs.m.prompt)
			}
			if !strings.Contains(plainScreen(hs.m), test.want+" (default)") || len(hs.be.newArgs) != 0 || len(hs.be.attached) != 0 {
				t.Fatal("default selection was not visible, or started work")
			}
			for _, count := range hs.be.dials {
				if count != 1 {
					t.Fatal("local default resolution dialled or woke a host", hs.be.dials)
				}
			}
		})
	}
}

func TestLauncherDoesNotFallBackFromASharedDefaultToTheHighlightedHost(t *testing.T) {
	hs := newPlacementFixture(t, "atlas-dev")
	if err := client.SetTierContext(t.Context(), "gcp-dev", client.TierShared, strings.Repeat("b", 32), ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv(brand.Env("DEFAULT_HOST"), "gcp-dev")
	hs.key("n")
	if hs.m.modal != nil || hs.m.prompt != nil || len(hs.be.newArgs) != 0 {
		t.Fatal("a refused default became another host's form or start")
	}
	if text := strings.Join(toastTexts(hs.m), "\n"); !strings.Contains(text, "shared host") || !strings.Contains(text, "cannot be the default") {
		t.Fatalf("the actual default refusal was hidden: %q", text)
	}
}

func TestLauncherDefaultTakesPrecedenceOverARestoredSessionsView(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	// Restore's remembered row can choose a screen, never the new target.
	hs.m.mem.Last = &lastSession{Host: "atlas-dev", ID: "work2345"}
	hs.m.screen, hs.m.cur = scrSessions, "atlas-dev"
	hs.key("n")
	dialog, ok := hs.m.modal.(*newSession)
	if !ok || dialog.host != "gcp-dev" || dialog.selector != "" || hs.m.prompt != nil {
		t.Fatal("a restored last-host screen overrode the configured default")
	}
}

func TestNewFormHostRowChangesOnlyThisSelectionAndStartsOnThatHost(t *testing.T) {
	hs := newPlacementFixture(t, "atlas-dev")
	hs.key("n")
	dialog, ok := hs.m.modal.(*newSession)
	if !ok {
		t.Fatalf("default did not open the form: %T", hs.m.modal)
	}
	dialog.name.set("chosen-work")
	dialog.folder.set("~/project")
	dialog.field = 6
	hs.key("right", "right")
	if dialog.host != "gcp-dev" || dialog.selector != "gcp-dev" || dialog.sourceHost.Name != "gcp-dev" || dialog.name.value() != "chosen-work" || dialog.folder.value() != "~/project" {
		t.Fatal("host selection lost the chosen saved entry or edited fields")
	}
	if got, err := client.DefaultHost(t.Context()); err != nil || got != "atlas-dev" {
		t.Fatal("the host row wrote a new default", got, err)
	}
	hs.key("ctrl+enter")
	if len(hs.be.newArgs) != 1 || hs.be.newArgs[0].Name != "chosen-work" || len(hs.be.attached) != 0 {
		t.Fatalf("the form did not use the normal detached start: args=%+v", hs.be.newArgs)
	}
	found := false
	for _, call := range hs.be.callList() {
		found = found || strings.HasPrefix(call, "new gcp-dev agent=")
	}
	if !found {
		t.Fatal("the form started on a different host", hs.be.callList())
	}
}

func TestNewFormRechecksItsDefaultAndSavedEntryBeforeStarting(t *testing.T) {
	for _, change := range []string{"default", "entry"} {
		t.Run(change, func(t *testing.T) {
			hs := newPlacementFixture(t, "gcp-dev")
			hs.key("n")
			dialog := hs.m.modal.(*newSession)
			dialog.name.set("no-start")
			if change == "default" {
				if err := client.SetDefaultHost(t.Context(), "atlas-dev"); err != nil {
					t.Fatal(err)
				}
			} else {
				hosts, err := client.LoadHosts()
				if err != nil {
					t.Fatal(err)
				}
				for i := range hosts {
					if hosts[i].Name == "gcp-dev" {
						hosts[i].SSH = "changed.example"
					}
				}
				if err := client.SaveHosts(t.Context(), hosts); err != nil {
					t.Fatal(err)
				}
			}
			hs.key("ctrl+enter")
			if len(hs.be.newArgs) != 0 || len(hs.be.attached) != 0 || dialog.err == nil || dialog.starting {
				t.Fatalf("a changed selection reached start or stayed busy: %v", dialog.err)
			}
		})
	}
}
