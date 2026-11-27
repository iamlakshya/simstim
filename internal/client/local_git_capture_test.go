package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalGitIncompleteRetainsSeedAndPlan(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "input")
	if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := &localGitOwner{done: make(chan struct{})}
	ownership := &localGitOwnership{}
	ownership.retain(owner)
	seed := &laptopSeed{directory: directory, ownership: ownership}
	plan := &SessionInputPlan{seedOrder: []*laptopSeed{seed}}
	for range 2 {
		var incomplete *localGitIncomplete
		if err := plan.Close(); !errors.As(err, &incomplete) || incomplete.owner != owner {
			t.Fatalf("actual unfinished owner was lost: %v", err)
		}
		if _, err := os.Stat(marker); err != nil || plan.closed {
			t.Fatal("unfinished native inputs were removed or plan made uncloseable")
		}
	}
	owner.known = true
	close(owner.done)
	if err := plan.Close(); err != nil || !plan.closed {
		t.Fatalf("joined cleanup cannot finish: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("joined owned capture remained")
	}
}

func TestLocalGitDeadOwnerWithoutAbsenceKeepsCapture(t *testing.T) {
	directory := t.TempDir()
	owner := &localGitOwner{done: make(chan struct{})}
	close(owner.done) // helper death is not an absence acknowledgement
	ownership := &localGitOwnership{}
	ownership.retain(owner)
	download := &downloadedPull{directory: directory, ownership: ownership}
	var incomplete *localGitIncomplete
	if err := download.close(); !errors.As(err, &incomplete) || incomplete.owner != owner {
		t.Fatalf("dead-owner uncertainty was lost: %v", err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatal("uncertain capture was unlinked")
	}
}
