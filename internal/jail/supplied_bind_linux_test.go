package jail

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSuppliedBindKeepsOriginalDescriptorAtAnotherTarget(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	want, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	checked, err := check(Spec{Argv: []string{target}, Seccomp: ProfileConverter, Binds: []Bind{{Path: target, File: file}}})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := pin(checked, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.close()
	got, err := pinned.files[0].Stat()
	if err != nil || !os.SameFile(want, got) || !pinned.binds[0].SuppliedV1 {
		t.Fatalf("the fixed target replaced its actual supplied source: %v", err)
	}
	if err := os.Rename(source, source+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The real child receives this exact owned source in its assigned slot.
	// Rebase only the fixture's child number to that same actual local handle.
	bound := pinned.binds[0]
	bound.FD = int(pinned.files[0].Fd())
	fds, err := reopen([]bind{bound})
	if err != nil {
		t.Fatal(err)
	}
	kept := os.NewFile(uintptr(fds[0]), "kept-supplied-bind")
	defer kept.Close()
	data, err := io.ReadAll(kept)
	if err != nil || string(data) != "original" {
		t.Fatalf("source pathname was reopened as new authority: %q, %v", data, err)
	}
}

func TestSuppliedBindStageCannotBecomeLegacyPathBinding(t *testing.T) {
	set := &setup{BindsV1Version: 1, Binds: []bind{{SuppliedV1: true}}}
	for _, stage := range []string{stageInit, stageInitFilesV1, stageExec} {
		if _, err := checkFileRulesStageV1(stage, set); err == nil {
			t.Fatalf("legacy stage %s accepted supplied bind capability", stage)
		}
	}
	if got, err := checkFileRulesStageV1(stageInitBindsV1, set); err != nil || got != stageInit {
		t.Fatalf("the exact supplied-bind stage did not validate: %q, %v", got, err)
	}
	set.BindsV1Version = 2
	if _, err := checkFileRulesStageV1(stageInitBindsV1, set); err == nil {
		t.Fatal("future supplied-bind setup became current authority")
	}
}
