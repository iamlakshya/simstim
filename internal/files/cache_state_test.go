//go:build linux || darwin

package files_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/sandbox"
)

func TestUncleanFileCaptureDoesNotDiscardOrPublishPrivateCacheState(t *testing.T) {
	yes := true
	plan, err := sandbox.CacheStatePlan(files.StatePlan{Files: []string{".agent/history"}}, &api.SandboxValues{KeepCaches: &yes, Caches: []string{".cache/tool"}})
	if err != nil {
		t.Fatal(err)
	}
	session := t.TempDir()
	cache := filepath.Join(session, "state/.cache/tool/content")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("untrusted cache data"), 0o700); err != nil {
		t.Fatal(err)
	}
	capture, err := files.BeginCapture(session, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(capture.Incoming, ".agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capture.Incoming, ".agent/history"), []byte("unproved new history"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := capture.Commit(false, capture.Nonce); err == nil {
		t.Fatal("an unclean exit published a file generation")
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(session, "state-files/CURRENT")); !os.IsNotExist(err) {
		t.Fatal("unclean cache persistence became file publication authority", err)
	}
	if body, err := os.ReadFile(cache); err != nil || string(body) != "untrusted cache data" {
		t.Fatal("discarding an incoming file generation removed the in-place cache", err)
	}
	next, err := files.BeginCapture(session, plan)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if _, err := os.Stat(filepath.Join(next.Restore, ".agent/history")); !os.IsNotExist(err) {
		t.Fatal("an unclean file generation was restored with cache state", err)
	}
}
