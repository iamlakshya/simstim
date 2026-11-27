//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These fixtures test the refusal-only observer. They cannot produce a
// continuation: no outside OP, holder, kernel epoch or native creator exists.
func noneActorFixtureV1(t *testing.T, path string, pid int, birth string, argv []string, unreadable bool) {
	t.Helper()
	dir := filepath.Join(path, fmt.Sprint(pid))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\n", os.Getuid(), os.Getuid(), os.Getuid(), os.Getuid())), 0600); err != nil {
		t.Fatal(err)
	}
	fields := append([]string{"S"}, make([]string, 18)...)
	for i := 1; i < len(fields); i++ {
		fields[i] = "0"
	}
	fields = append(fields, birth)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(fmt.Sprintf("%d (role with )) %s\n", pid, strings.Join(fields, " "))), 0600); err != nil {
		t.Fatal(err)
	}
	if unreadable {
		if err := os.Mkdir(filepath.Join(dir, "cmdline"), 0700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNoneColdScanDoesNotBlockOtherHealthyOrUnreadableND0Processes(t *testing.T) {
	dir := t.TempDir()
	source := noneCleanFixtureV1()
	noneActorFixtureV1(t, dir, 2001, "80", []string{"helper", "supervise", "b2b2b2b2"}, false)
	noneActorFixtureV1(t, dir, 2002, "81", nil, true)
	// The former supervisor number now belongs to an unreadable unrelated
	// actor with a different kernel birth. It is not this original run.
	noneActorFixtureV1(t, dir, 1001, "999", nil, true)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := scanNoneColdActorsV1(context.Background(), root, &source); err != nil {
		t.Fatal(err)
	}
}

func TestNoneColdScanRefusesExactOrUnknownOriginalActors(t *testing.T) {
	for _, kind := range []string{"supervise", "supervise-restore-v1", "holder", "wall-init", "original-birth", "original-unknown", "oversized-matching"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source := noneCleanFixtureV1()
			argv := []string{"unknown-code", kind, source.ID}
			pid, birth := 3001, "90"
			unknown := false
			if kind == "wall-init" {
				argv = []string{"unknown-code", kind, "--wall", "none", source.ID}
			}
			if kind == "original-birth" {
				pid, birth, unknown = 1001, "42", true
			}
			if kind == "original-unknown" {
				pid, birth, unknown = 1001, "not-a-birth", true
			}
			if kind == "oversized-matching" {
				argv = []string{"unknown-code", "supervise", source.ID, strings.Repeat("x", noneActorCommandBoundV1)}
			}
			noneActorFixtureV1(t, dir, pid, birth, argv, unknown)
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := scanNoneColdActorsV1(context.Background(), root, &source); err == nil {
				t.Fatal("a credible target actor was ignored")
			}
		})
	}
}

func TestNoneColdScanCancellationIsNotVacancy(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := noneCleanFixtureV1()
	if err := scanNoneColdActorsV1(ctx, root, &source); err == nil {
		t.Fatal("cancellation became vacant runtime data")
	}
}

func TestNoneColdScanIncludesOriginalRootedMemberIDs(t *testing.T) {
	dir := t.TempDir()
	source := noneCleanFixtureV1()
	member := "b2b2b2b2"
	noneActorFixtureV1(t, dir, 4001, "90", []string{"helper", "holder", member}, false)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := scanNoneColdActorsV1(context.Background(), root, &source, member); err == nil {
		t.Fatal("a current holder of an original rooted member was ignored")
	}
}

func TestNoneColdScanUsesKernelAccountRatherThanProcDirectoryOwnership(t *testing.T) {
	dir := t.TempDir()
	source := noneCleanFixtureV1()
	noneActorFixtureV1(t, dir, 5001, "90", []string{"helper", "supervise", source.ID}, false)
	// A reliably foreign kernel account does not become this target just
	// because the fixture directory is owned by the test's current account.
	foreign := uint64(os.Getuid()) + 1
	status := filepath.Join(dir, "5001", "status")
	if err := os.WriteFile(status, []byte(fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\n", foreign, foreign, foreign, foreign)), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := scanNoneColdActorsV1(context.Background(), root, &source); err != nil {
		t.Fatal("a foreign account was selected from directory ownership", err)
	}
	// An unknown current account with a credible target command still
	// refuses. Unknown does not excuse a matching actor.
	if err := os.Remove(status); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(status, 0700); err != nil {
		t.Fatal(err)
	}
	if err := scanNoneColdActorsV1(context.Background(), root, &source); err == nil {
		t.Fatal("an unknown matching target was ignored")
	}
}
