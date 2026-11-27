package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestKnownSessionIDNeverFallsBackToName(t *testing.T) {
	for _, id := range []string{"legacyid", "abc234ef", strings.Repeat("a", 32)} {
		t.Run(id, func(t *testing.T) {
			collision := []client.Session{{ID: "owner234", Name: id}}
			if s, err := resolveIDIn(collision, id, "box"); s != nil || err == nil {
				t.Fatalf("vanished ID resolved as name: %+v, %v", s, err)
			}
			s, err := resolveIDIn(append(collision, client.Session{ID: id, Name: "mine"}), id, "box")
			if err != nil || s.ID != id {
				t.Fatalf("present exact ID: %+v, %v", s, err)
			}
		})
	}
}

func TestIDCommandsValidateBeforeDial(t *testing.T) {
	for _, verb := range []string{"attach", "pull"} {
		for _, id := range []string{"good1\u200b", "bad\nline", "id; command", strings.Repeat("a", 65)} {
			t.Run(verb+id, func(t *testing.T) {
				_, tr := fakeBox(t, nil)
				// pull's local git preflight must succeed, without using a real repo.
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				_, errOut, code := mainRun(t, verb, "--id", "--", "box/"+id)
				if code != 2 || !strings.Contains(errOut, "session ID") || !strings.Contains(errOut, "Try:") {
					t.Fatalf("exit %d: %s", code, errOut)
				}
				if len(tr.Remotes()) != 0 || len(tr.Ops()) != 0 {
					t.Fatalf("invalid raw ID reached host: %v, %v", tr.Remotes(), tr.Ops())
				}
			})
		}
	}
}

func TestAttachLastAndIDDoNotSelectANameCollision(t *testing.T) {
	oldTTY := isTerminal
	isTerminal = func(*os.File) bool { return true }
	t.Cleanup(func() { isTerminal = oldTTY })
	_, tr := fakeBox(t, []client.Session{{ID: "owner234", Name: "legacyid", State: api.StateIdle}})
	client.RememberLast("box", "legacyid", "mine")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"attach", "-"}, "is gone"},
		{[]string{"attach", "--id", "--", "box/legacyid"}, "no session"},
	} {
		_, errOut, code := mainRun(t, tc.args...)
		if code != 3 || !strings.Contains(errOut, tc.want) {
			t.Fatalf("%v: exit %d, %s", tc.args, code, errOut)
		}
	}
	if slices.Contains(tr.Ops(), "attach") {
		t.Fatal("attached to name collision")
	}
	// The literal target '-' under --id is never the last-session alias.
	_, _, code := mainRun(t, "attach", "--id", "--", "-")
	if code != 2 {
		t.Fatalf("--id - exit %d", code)
	}
	_, errOut, code := mainRun(t, "attach", "--id")
	if code != 2 || !strings.Contains(errOut, "a session ID") {
		t.Fatalf("--id without target: %d, %s", code, errOut)
	}
}

func TestIDLookupRejectsNonBase32LeadingDashTarget(t *testing.T) {
	_, tr := fakeBox(t, []client.Session{{ID: "old234", State: api.StateIdle}})
	cmd := pullCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("id", "true"); err != nil {
		t.Fatal(err)
	}
	err := withSession(cmd, "box/-old", func(_ context.Context, _ client.Host, _ *client.Conn, _ *client.Session) error {
		return nil
	})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "bad_session_id" || len(tr.Ops()) != 0 {
		t.Fatalf("non-base32 leading dash ID: %v, ops=%v", err, tr.Ops())
	}
}

func TestMainNoInstallEnvironmentFailsBeforeProbe(t *testing.T) {
	for _, value := range []string{"1", "0", "true", ""} {
		t.Run(value, func(t *testing.T) {
			_, tr := fakeBox(t, nil)
			tr.BridgeStderr = "sh: helper: No such file or directory"
			t.Setenv(brand.Env("NO_INSTALL"), value)
			_, errOut, code := mainRun(t, "ls", "--host", "box")
			// ls reports unavailable hosts as rows, while preserving non-mutation.
			if code != 0 {
				t.Fatalf("ls exit %d: %s", code, errOut)
			}
			probed := false
			for _, r := range tr.Remotes() {
				if strings.Contains(r, "uname -sm") {
					probed = true
				}
			}
			if probed == (value == "1") {
				t.Fatalf("NO_INSTALL=%q: probe=%v, commands %q", value, probed, tr.Remotes())
			}
		})
	}
}

func TestResolveIDReturnsNotFoundKind(t *testing.T) {
	_, err := resolveIDIn(nil, "legacy", "box")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "session_not_found" || ue.Kind != client.KindNotFound {
		t.Fatalf("%+v", err)
	}
}
