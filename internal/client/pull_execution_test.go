package client

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundleReviewAndBranchUpdateRunNoHookFilterOrTextconv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the execution marker uses a POSIX script")
	}
	fixture := makeNativePull(t, ".gitattributes", "*.txt filter=fixture diff=fixture\n", false)
	directory := t.TempDir()
	marker := filepath.Join(directory, "executed")
	t.Setenv("PULL_EXECUTION_MARKER", marker)
	script := filepath.Join(directory, "unexpected-command")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf executed > \"$PULL_EXECUTION_MARKER\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(directory, "hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(hooks, "reference-transaction"), data, 0700); err != nil {
		t.Fatal(err)
	}
	pullFixtureGit(t, fixture.local, "config", "core.hooksPath", hooks)
	pullFixtureGit(t, fixture.local, "config", "filter.fixture.process", script)
	pullFixtureGit(t, fixture.local, "config", "filter.fixture.required", "true")
	pullFixtureGit(t, fixture.local, "config", "diff.fixture.textconv", script)
	pullFixtureGit(t, fixture.local, "config", "diff.external", script)
	peer := nativePullPeer(fixture)
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", AllowFlagged: true})
	if err != nil || result == nil || !result.Moved {
		t.Fatal("safe data-only positive control failed", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("pull ran a hook, filter, external diff or textconv")
	}
	if _, err := os.Stat(filepath.Join(fixture.local, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatal("pull checked out the guest attributes")
	}
}
