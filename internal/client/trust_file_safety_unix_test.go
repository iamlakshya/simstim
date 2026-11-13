//go:build !windows

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupTrustPreservesForeignSymlinkTemp(t *testing.T) {
	testForeignTrustTemp(t, func(t *testing.T, path string) func() {
		t.Helper()
		target := filepath.Join(t.TempDir(), "foreign-file")
		const want = "a different writer owns this symlink target"
		if err := os.WriteFile(target, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		beforeLink, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		beforeTarget, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		return func() {
			t.Helper()
			afterLink, err := os.Lstat(path)
			if err != nil || afterLink.Mode()&os.ModeSymlink == 0 || !os.SameFile(beforeLink, afterLink) {
				t.Errorf("save removed or replaced the foreign symlink: %v", err)
			} else if got, err := os.Readlink(path); err != nil || got != target {
				t.Errorf("save changed the foreign symlink: %q, %v", got, err)
			}
			afterTarget, err := os.Stat(target)
			if err != nil || !os.SameFile(beforeTarget, afterTarget) {
				t.Errorf("save removed or replaced the foreign target: %v", err)
				return
			}
			if got := trustSafetyRead(t, target); string(got) != want {
				t.Errorf("save overwrote the foreign symlink target: %q", got)
			}
		}
	})
}

// Real mode bits exercise the regular-file permission failure for a normal
// Unix user. The always-run directory fixture exercises the same read-error
// contract when elevated privileges bypass these mode bits.
func TestSetupTrustPreservesAnUnreadableRegularFile(t *testing.T) {
	before := seedTrustSafety(t)
	if err := os.Chmod(trustPath(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(trustPath(), 0o600) })
	if _, err := os.ReadFile(trustPath()); err == nil {
		t.Log("this user can read mode 000 files; TestSetupTrustFailsClosedWhenTheAnswersCannotBeRead supplies the deterministic read-failure evidence")
		return
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("mode 000 fixture has a different read failure: %v", err)
	}
	names := trustSafetyEntries(t)
	answers := LoadSetupAnswers("box")
	if len(answers.Refused)+len(answers.Hashes)+len(answers.Trusted) != 0 {
		t.Errorf("unreadable regular file trusted answers: %+v", answers)
	}
	assertTrustReadFailure(t, answers.Warning)
	for _, op := range []struct {
		name     string
		remember func() (*UserError, error)
	}{
		{"context", func() (*UserError, error) {
			return RememberSetupContext(context.Background(), "box", "/new", true, "new-hash")
		}},
		{"legacy", func() (*UserError, error) { return RememberSetup("box", "/new", true, "new-hash") }},
	} {
		warn, err := op.remember()
		if warn != nil {
			t.Errorf("%s returned a recovery warning for an unreadable file: %v", op.name, warn)
		}
		assertTrustReadFailure(t, err)
	}
	if err := os.Chmod(trustPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := trustSafetyRead(t, trustPath()); string(got) != string(before) {
		t.Errorf("read failure replaced existing refusals or hashes: %q", got)
	}
	assertTrustSafetyEntries(t, names)
	kept := LoadSetupAnswers("box")
	refused, found := kept.Refused["/refused"]
	if !found || refused || !kept.Trusted["/trusted"] || kept.Hashes["/trusted"] != "kept-hash" || kept.Trusted["/new"] {
		t.Errorf("read failure changed the saved answers: %+v", kept)
	}
}
