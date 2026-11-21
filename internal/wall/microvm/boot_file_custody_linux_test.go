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
