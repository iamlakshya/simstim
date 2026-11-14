package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func capturedOrphanConfig(t *testing.T) (ownedSSHConfig, localFix) {
	t.Helper()
	ownLockFiles(t, time.Millisecond, time.Second)
	if err := SaveHosts(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	path := wrapperPath(Host{Name: "old"})
	if _, err := writeConfig(path, `host "old"`, "", true); err != nil {
		t.Fatal(err)
	}
	candidate, ok := snapshotGeneratedSSHConfig(path)
	if !ok {
		t.Fatal("generated orphan was not captured")
	}
	fixes := sshConfigFix()
	if len(fixes) != 1 {
		t.Fatalf("orphan fixes = %d", len(fixes))
	}
	return candidate, fixes[0]
}

func TestDoctorRechecksSavedReferencesAfterTheQuestion(t *testing.T) {
	candidate, fix := capturedOrphanConfig(t)
	if err := SaveHosts(context.Background(), []Host{{Name: "current", SSH: "current", SSHConfig: candidate.path}}); err != nil {
		t.Fatal(err)
	}
	if err := fix.run(context.Background(), &Fix{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(candidate.path); err != nil {
		t.Fatalf("doctor removed a config referenced after its question: %v", err)
	}
}

func TestDoctorKeepsAnInodeOrHeaderReplacedAfterItsQuestion(t *testing.T) {
	for _, replacement := range []string{"owned new inode", "user header"} {
		t.Run(replacement, func(t *testing.T) {
			candidate, fix := capturedOrphanConfig(t)
			contents := []byte("Host custom\n")
			if replacement == "owned new inode" {
				// Reuse the valid ownership header while replacing the inode.
				old, err := os.ReadFile(candidate.path)
				if err != nil {
					t.Fatal(err)
				}
				contents = append(old, []byte("# replacement\n")...)
				if err := writeAtomicFile(candidate.path, ".replacement-*", contents); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(candidate.path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := fix.run(context.Background(), &Fix{}); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(candidate.path)
			if err != nil || string(actual) != string(contents) {
				t.Fatalf("doctor removed a replacement config: %v", err)
			}
		})
	}
}

func TestDoctorProtectsAReferenceThroughAnAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs privileges on Windows")
	}
	candidate, fix := capturedOrphanConfig(t)
	alias := filepath.Join(t.TempDir(), "included")
	if err := os.Symlink(candidate.path, alias); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(context.Background(), []Host{{Name: "current", SSH: "current", SSHConfig: alias}}); err != nil {
		t.Fatal(err)
	}
	if err := fix.run(context.Background(), &Fix{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(alias); err != nil {
		t.Fatalf("doctor broke a saved config's alias: %v", err)
	}
}

func TestDoctorRefusesUnreadableCurrentMembership(t *testing.T) {
	candidate, fix := capturedOrphanConfig(t)
	if err := os.WriteFile(hostsPath(), []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var user *UserError
	if err := fix.run(context.Background(), &Fix{}); !errors.As(err, &user) || user.Code != "bad_config" {
		t.Fatalf("doctor ignored current membership failure: %v", err)
	}
	if _, err := os.Stat(candidate.path); err != nil {
		t.Fatalf("doctor unlinked after a membership error: %v", err)
	}
}

func TestDoctorCleanupLockWaitHonoursCancellation(t *testing.T) {
	candidate, fix := capturedOrphanConfig(t)
	unlock, err := lockHostsFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := fix.run(ctx, &Fix{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("doctor lock wait = %v", err)
	}
	if _, err := os.Stat(candidate.path); err != nil {
		t.Fatalf("cancelled cleanup unlinked its file: %v", err)
	}
}

func TestDoctorUnlinkHoldsTheCanonicalLocks(t *testing.T) {
	candidate, fix := capturedOrphanConfig(t)
	previous := beforeSSHConfigSnapshotOpen
	checked := false
	beforeSSHConfigSnapshotOpen = func(path string) {
		if path == candidate.path {
			checked = true
			assertHostFileLockHeld(t, lockFile(candidate.owner))
			assertHostFileLockHeld(t, hostsPath()+".lock")
		}
	}
	t.Cleanup(func() { beforeSSHConfigSnapshotOpen = previous })
	if err := fix.run(context.Background(), &Fix{}); err != nil || !checked {
		t.Fatalf("owned unlink lock witness: checked=%v err=%v", checked, err)
	}
	if _, err := os.Stat(candidate.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unreferenced unchanged owned config was not removed: %v", err)
	}
}
