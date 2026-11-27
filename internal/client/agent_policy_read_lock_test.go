//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentPolicySnapshotSharesTheCrossProcessStateLock(t *testing.T) {
	if os.Getenv("H5_POLICY_READ_MAX") != "1" {
		t.Skip("set H5_POLICY_READ_MAX=1 in the admitted state-lock proof job")
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	host := Host{Name: "box"}
	path := filepath.Join(StateDir(), "hosts", HostFileKey(host.Name), "sandboxes.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, projectionState(t, 2, projectionRow(), nil), 0o600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	err = withStateFile(t.Context(), []string{"hosts", HostFileKey(host.Name)}, "sandboxes.json", false,
		func([]byte, bool) ([]byte, error) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "-test.run=^TestAgentPolicySnapshotLockChild$")
			command.Env = append(os.Environ(), "H5_POLICY_LOCK_CHILD=1", "H5_POLICY_LOCK_STATE="+state)
			command.WaitDelay = 2 * time.Second
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("owned snapshot child did not observe the canonical lock: %v (%s)", err, output)
			}
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentPolicySnapshotLockChild(t *testing.T) {
	if os.Getenv("H5_POLICY_READ_MAX") != "1" || os.Getenv("H5_POLICY_LOCK_CHILD") != "1" {
		t.Skip("owned child of the admitted private state-lock fixture")
	}
	// TestMain already isolates HOME, USERPROFILE and all XDG locations.
	// Only the parent's explicitly generated private state fixture is shared.
	state := os.Getenv("H5_POLICY_LOCK_STATE")
	info, err := os.Lstat(state)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("snapshot child did not receive a private fixture directory")
	}
	t.Setenv("XDG_STATE_HOME", state)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	policy, err := readAgentSandboxPolicy(ctx, Host{Name: "box"}, projectionSession)
	var busy *UserError
	blocked := errors.Is(err, context.DeadlineExceeded) || errors.As(err, &busy) && busy.Code == "grant_busy"
	if policy != nil || !blocked {
		t.Fatalf("snapshot bypassed the parent's canonical state lock: policy=%+v error=%v", policy, err)
	}
}
