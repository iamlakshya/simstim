//go:build linux

package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

type bootLogicalFailureV2 struct {
	failure error
	calls   int
}

func (owner *bootLogicalFailureV2) Close() error { owner.calls++; return owner.failure }

func TestBootFilesRetainActualStoreUnderUnchangedIOFailureV2(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	store, err := runtimecache.Open(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "input"))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("original sync failed")
	original := &bootLogicalFailureV2{failure: failure}
	files := &bootFiles{tools: file, agentsLease: original, runtimeStore: store}
	if err := files.close(); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if files.tools != nil || files.agentsLease != original || files.runtimeStore != store {
		t.Fatal("failed cleanup dropped a lower original owner or repeated completed work")
	}
	if err := files.close(); !errors.Is(err, failure) {
		t.Fatal("descriptor retirement erased an independent IO failure", err)
	}
	if original.calls != 2 {
		t.Fatal("retry did not call the SAME original logical close")
	}
	original.failure = nil
	if err := files.close(); err != nil {
		t.Fatal(err)
	}
	if files.runtimeStore != nil || files.agentsLease != nil {
		t.Fatal("successful exact retry did not finish ordered cleanup")
	}
}

func TestBootFilesUnknownDescriptorCloseRetainsOriginalStackV2(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	store, err := runtimecache.Open(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	file, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	files := &bootFiles{tools: file, runtimeStore: store}
	if err := files.close(); err == nil {
		t.Fatal("already-closed was treated as a genuine first retirement")
	}
	if files.tools != file || files.runtimeStore != store {
		t.Fatal("unknown close lost the SAME actual objects")
	}
	if err := files.close(); err == nil {
		t.Fatal("unknown close became cleanup success")
	}
}

func TestBootFileCustodyRefusesUnknownNativeBorrowerV2(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	file, err := os.Create(filepath.Join(t.TempDir(), "retained"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	files := &bootFiles{tools: file}
	sandbox := &sandbox{}
	if err := sandbox.adoptBootFilesV2(files); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.adoptBootFilesV2(&bootFiles{}); err == nil {
		t.Fatal("another factory replaced the retained original")
	}
	if err := sandbox.attachBootFileCommandV2(&jail.Cmd{}); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.releaseBootFilesV2(); err == nil {
		t.Fatal("unknown native closure released the original files")
	}
	if sandbox.bootCustody == nil || sandbox.bootCustody.files != files {
		t.Fatal("unknown physical result dropped custody")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("unknown native closure touched its borrowed file", err)
	}
}

// Root supplies a genuine filesystem whose concrete first Close returns a
// late error. This test never injects a callback, errno or retirement result.
func TestBootFileDiagnosticsSurviveActualRetirementAndCustodyRemovalV2(t *testing.T) {
	path := os.Getenv("VM_REAL_LATE_CLOSE_FILE")
	if path == "" {
		t.Skip("Root must admit the real native late-close fixture")
	}
	unfilteredRetirementFixtureV2(t)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("the admitted real close fixture path is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	files := &bootFiles{tools: file}
	sandbox := &sandbox{}
	if err := sandbox.adoptBootFilesV2(files); err != nil {
		t.Fatal(err)
	}
	first := sandbox.releaseBootFilesV2()
	if first == nil || files.retirements[file] == nil || !files.retirements[file].retired {
		t.Fatal("fixture did not produce a genuine retired first-close error", first)
	}
	if !errors.Is(sandbox.PreparedCloseDiagnosticsV2(), first) {
		t.Fatal("failed cleanup erased its original diagnostic")
	}
	if err := sandbox.releaseBootFilesV2(); err != nil || sandbox.bootCustody != nil {
		t.Fatal("actual retired retry did not finish its original custody", err)
	}
	if !errors.Is(sandbox.PreparedCloseDiagnosticsV2(), first) {
		t.Fatal("successful physical cleanup erased the first I/O outcome")
	}
	if err := sandbox.releaseBootFilesV2(); err != nil || !errors.Is(sandbox.PreparedCloseDiagnosticsV2(), first) {
		t.Fatal("later cleanup changed the retained first outcome", err)
	}
}

func TestBootFileDiagnosticsNeverGrantUnknownRetirementV2(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	file, err := os.Create(filepath.Join(t.TempDir(), "already-closed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	files := &bootFiles{tools: file}
	sandbox := &sandbox{}
	if err := sandbox.adoptBootFilesV2(files); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := sandbox.releaseBootFilesV2(); err == nil || sandbox.bootCustody == nil || files.tools != file {
			t.Fatal("a report-only diagnostic granted unknown cleanup", err)
		}
		if !errors.Is(sandbox.PreparedCloseDiagnosticsV2(), os.ErrClosed) {
			t.Fatal("actual unknown-close diagnostic was lost")
		}
	}
}
