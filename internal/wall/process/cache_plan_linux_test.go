//go:build linux

package process_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/sandbox"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// This consumes the canonical P16 MountPlan API, with no launcher or mount.
func cacheMountPlan(t *testing.T, session string, values *api.SandboxValues) []wall.Mount {
	t.Helper()
	state, err := sandbox.CacheStatePlan(files.StatePlan{}, values)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := process.MountPlan(wall.Spec{SessionDir: session, Helper: t.TempDir(), StateDirs: state.Dirs})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestProcessCachesUsePrivatePrimaryRootsAndSurvivePlanRestart(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, session := range []string{first, second} {
		canonical, err := filepath.EvalSymlinks(session)
		if err != nil {
			t.Fatal(err)
		}
		for _, mount := range cacheMountPlan(t, session, nil) {
			name, cache := strings.CutPrefix(mount.Guest, "/home/agent/")
			if !cache {
				continue
			}
			want := filepath.Join(canonical, "state", filepath.FromSlash(name))
			if !mount.Write || mount.Host != want || name == ".cargo/bin" {
				t.Fatal("cache did not use its own private writable state source", mount)
			}
		}
	}
	name := filepath.Join(first, "state/.npm/_cacache/content")
	if err := os.WriteFile(name, []byte("retained after unclean end"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheMountPlan(t, first, nil)
	if body, err := os.ReadFile(name); err != nil || string(body) != "retained after unclean end" {
		t.Fatal("in-place cache changed on the next start plan", err)
	}
	if _, err := os.Stat(filepath.Join(second, "state/.npm/_cacache/content")); !os.IsNotExist(err) {
		t.Fatal("another primary sandbox saw cached bytes", err)
	}
}

func TestProcessCacheFalseAndExactDropRemoveBindsWithoutDeletingState(t *testing.T) {
	session := t.TempDir()
	cacheMountPlan(t, session, nil)
	yes, no := true, false
	roots, err := sandbox.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range []*api.SandboxValues{{KeepCaches: &no, Caches: roots}, {KeepCaches: &yes, Caches: slices.DeleteFunc(slices.Clone(roots), func(name string) bool { return name == ".cache/pip" })}} {
		for _, mount := range cacheMountPlan(t, session, values) {
			if strings.HasPrefix(mount.Guest, "/home/agent/") && (!*values.KeepCaches || mount.Guest == "/home/agent/.cache/pip") {
				t.Fatal("disabled or dropped cache still bound into the guest", mount)
			}
		}
	}
	if info, err := os.Stat(filepath.Join(session, "state/.cache/pip")); err != nil || !info.IsDir() {
		t.Fatal("changing the effective plan deleted the sandbox's stored data", err)
	}
}
