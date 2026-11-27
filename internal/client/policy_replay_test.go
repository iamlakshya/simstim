package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyDryRunSuppressesPendingReplayWithoutReadingInvalidState(t *testing.T) {
	isolateH5GrantHome(t)
	if err := os.MkdirAll(StateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2,"hosts":["box"],"future":true}`)
	path := filepath.Join(StateDir(), policyPendingFile)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReplayPendingPolicy(WithoutPolicyReplay(t.Context()), nil); err != nil {
		t.Fatal("read-only intent entered pending replay", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("dry run changed future pending state", err)
	}
}

func TestPolicyChangeRejectsReplacementStateRootBeforePendingPublication(t *testing.T) {
	isolateH5GrantHome(t)
	change, err := BeginPolicyChange(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer change.Close()
	ctx := change.Context(t.Context())
	if err := os.Rename(StateDir(), StateDir()+"-previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(StateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(StateDir(), "foreign")
	if err := os.WriteFile(marker, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := updatePolicyPending(ctx, nil, []string{"box"}); err == nil {
		t.Fatal("old policy lock adopted a replacement state directory")
	}
	if _, err := os.Lstat(filepath.Join(StateDir(), policyPendingFile)); !os.IsNotExist(err) {
		t.Fatal("old operation wrote into replacement state", err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "retain" {
		t.Fatal("failed publication changed foreign state", err)
	}
}
