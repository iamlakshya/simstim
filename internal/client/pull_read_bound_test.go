package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client/review"
)

func TestReviewLargeObjectsNeverLaunchContentReaderOrDiff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command recorder uses the local POSIX shell")
	}
	fixture := makeNativePull(t, "large.dat", strings.Repeat("x", int(review.FileLimit)+1), false)
	repo, err := openPullGit(t.Context(), fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := repo.changedObjects(t.Context(), fixture.base, fixture.tip)
	if err != nil || len(objects) != 1 {
		t.Fatalf("native changed-object positive control: %v %v", objects, err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "commands")
	wrapper := filepath.Join(directory, "record-git")
	script := "#!/bin/sh\nprintf '%s\\n' BEGIN \"$@\" END >> \"$PULL_COMMAND_MARKER\"\nexec \"$PULL_REAL_GIT\" \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	repo.executable = wrapper
	repo.env = append(repo.env, "PULL_COMMAND_MARKER="+marker, "PULL_REAL_GIT="+git)
	change, err := repo.reviewChange(t.Context(), fixture.base, fixture.tip, objects[0])
	if err != nil || change.Size != review.FileLimit+1 || len(change.After) != 0 || len(change.Before) != 0 || len(change.Added) != 0 {
		t.Fatalf("large-file content was loaded: %+v %v", change, err)
	}
	commands, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(commands), "cat-file\n-s\n") {
		t.Fatal("recorder's native size-probe positive control did not run", err)
	}
	for _, forbidden := range []string{"cat-file\nblob\n", "\ndiff\n", "\nconfig\n"} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatal("large changed object launched a content reader", forbidden)
		}
	}
	// A small replacement must not make the large OLD side readable either.
	pullFixtureCommit(t, fixture.source, "large.dat", "small replacement")
	smallTip := pullFixtureGit(t, fixture.source, "rev-parse", "HEAD")
	objects, err = repo.changedObjects(t.Context(), fixture.tip, smallTip)
	if err != nil || len(objects) != 1 {
		t.Fatal(objects, err)
	}
	if err = os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	change, err = repo.reviewChange(t.Context(), fixture.tip, smallTip, objects[0])
	if err != nil || change.Size != review.FileLimit+1 || len(change.Before) != 0 || len(change.After) != 0 {
		t.Fatal("small new side caused a large old-side read", change, err)
	}
	commands, err = os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"cat-file\nblob\n", "\ndiff\n", "\nconfig\n"} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatal("large old side launched content reader", forbidden)
		}
	}
}

func TestOversizedModuleFileIsRefusedWithoutBeingReadEvenWithConsent(t *testing.T) {
	fixture := makeNativePull(t, ".gitmodules", "#"+strings.Repeat("x", int(review.FileLimit)+1), false)
	peer := nativePullPeer(fixture)
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", AllowFlagged: true})
	if err == nil || result == nil || result.Moved {
		t.Fatal("oversized module content was approved or published", err)
	}
	if strings.Contains(localRefs(t, fixture.local), "refs/heads/review-target") {
		t.Fatal("oversized module file moved the branch")
	}
}
