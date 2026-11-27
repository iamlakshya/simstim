package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func resumePolicyOutside(t *testing.T) {
	t.Helper()
	old := SelfCgroup
	SelfCgroup = func() ([]byte, error) { return []byte(outsideCgroup("")), nil }
	t.Cleanup(func() { SelfCgroup = old })
	userManager(t, "cgroup:[1]", "cgroup:[1]", nil)
}

func resumePolicyLayers(target string) []api.Layer {
	layers := []api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{
		{Allow: true, Target: "once.example.com", Once: true, GrantID: "old-grant"},
		{Allow: true, Target: target},
	}}}
	if target == "locked.example.com" {
		layers = append([]api.Layer{{Kind: api.LayerAll, Lock: &api.Lock{Deny: []string{target}}}}, layers...)
	}
	return layers
}

func resumePolicyError(t *testing.T, err error, code, hint string) {
	t.Helper()
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != code || pe.Hint != hint {
		t.Fatalf("resume error = %v, want code %q and hint %q", err, code, hint)
	}
}

func TestResumeRejectsInvalidPolicyBeforeStoppingHolder(t *testing.T) {
	for _, tc := range []struct{ target, code, hint string }{
		{"invalid target", "bad_target", "Pass a host such as api.example.com, a wildcard such as *.example.com, or an address or CIDR."},
		{"locked.example.com", "policy_locked", "Remove locked.example.com from [lock] network.deny."},
	} {
		t.Run(tc.code, func(t *testing.T) {
			m := savedPolicyManifest(t)
			resumePolicyOutside(t)
			m.Layers = resumePolicyLayers(tc.target)
			m.Policy = &api.Policy{Preset: api.PresetOpen}
			m.Run.Unit, m.Run.RespawnCount = UnitName(m.ID), 3
			until := time.Now().Add(time.Hour)
			m.DebugUntil = &until
			if err := m.Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(paths.Manifest(m.ID))
			if err != nil {
				t.Fatal(err)
			}
			// These owned files expose stopHolder's socket cleanup without
			// starting a process or contacting a systemd user manager.
			for _, path := range []string{paths.Socket(m.ID), paths.HookSocket(m.ID)} {
				if err := paths.EnsurePrivateDir(filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			stops := fakeSystemctl(t, "not-found\n", nil)
			result, err := Resume(api.ResumeArgs{ID: m.ID, MemoryMax: api.MinMemoryMax})
			resumePolicyError(t, err, tc.code, tc.hint)
			if result != nil || len(*stops) != 0 {
				t.Fatalf("refused resume returned a result or stopped its old unit: %+v, %q", result, *stops)
			}
			after, err := os.ReadFile(paths.Manifest(m.ID))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("policy refusal changed saved policy, debug or lifecycle state: %v", err)
			}
			for _, path := range []string{paths.Socket(m.ID), paths.HookSocket(m.ID)} {
				if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
					t.Fatalf("policy refusal cleaned an old holder socket: %q, %v", data, err)
				}
			}
			unlock, err := lockSession(m.ID)
			if err != nil {
				t.Fatalf("policy refusal retained the session lock: %v", err)
			}
			unlock()
		})
	}
}

func TestResumePreservesPolicyErrorAfterHoldersLastSave(t *testing.T) {
	for _, tc := range []struct{ target, code, hint string }{
		{"invalid target", "bad_target", "Pass a host such as api.example.com, a wildcard such as *.example.com, or an address or CIDR."},
		{"locked.example.com", "policy_locked", "Remove locked.example.com from [lock] network.deny."},
	} {
		t.Run(tc.code, func(t *testing.T) {
			m := savedPolicyManifest(t)
			resumePolicyOutside(t)
			m.Run.Unit = UnitName(m.ID)
			if err := m.Save(); err != nil {
				t.Fatal(err)
			}
			fakeStopManager(t, true)
			oldRepoInfo := repositorySourceInfo
			repositorySourceInfo = func(string) (*api.RepoInfo, error) {
				return nil, UserError("not_a_repo", "not a git repository", "Start in a git repository.")
			}
			t.Cleanup(func() { repositorySourceInfo = oldRepoInfo })
			oldSystemctl := systemctl
			t.Cleanup(func() { systemctl = oldSystemctl })
			stops := 0
			var lastSave []byte
			systemctl = func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "stop":
					stops++
					// Model the old holder's final trusted manifest save.
					m.Layers = resumePolicyLayers(tc.target)
					m.Policy = &api.Policy{Preset: api.PresetOpen}
					m.Armed = true
					if err := m.Save(); err != nil {
						t.Fatal(err)
					}
					var err error
					lastSave, err = os.ReadFile(paths.Manifest(m.ID))
					if err != nil {
						t.Fatal(err)
					}
					return nil, nil
				case "show":
					if len(args) >= 4 && args[3] == "ActiveState" {
						return []byte("inactive\n"), nil
					}
					return []byte("not-found\n"), nil
				default:
					t.Fatalf("unexpected systemd action: %q", args)
					return nil, nil
				}
			}
			oldExecutable := holderExecutable
			launches := 0
			holderExecutable = func() (string, error) {
				launches++
				return "", errors.New("unexpected launch")
			}
			t.Cleanup(func() { holderExecutable = oldExecutable })
			result, err := Resume(api.ResumeArgs{ID: m.ID})
			resumePolicyError(t, err, tc.code, tc.hint)
			if result != nil || stops != 1 || launches != 0 {
				t.Fatalf("post-stop policy error was bypassed: result=%+v stops=%d launches=%d", result, stops, launches)
			}
			after, err := os.ReadFile(paths.Manifest(m.ID))
			if err != nil || len(lastSave) == 0 || !bytes.Equal(lastSave, after) {
				t.Fatalf("failed restart overwrote the old holder's last save: %v", err)
			}
		})
	}
}
