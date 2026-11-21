//go:build linux

package host

import (
	"bytes"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestRetiredCustodyConfigClosesOriginalUnlinkedInodeAndPreservesSubstitute(t *testing.T) {
	root, err := cachefs.OpenPrivateRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openCustodyConfigFileV1(root, "retired.conf", []byte("original\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	retired := &custodyRetiredConfigV1{file: file}
	if err := root.Remove("retired.conf"); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("preserve replacement\n")
	if err := root.WriteFile("retired.conf", replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retired.closeV1(); err != nil {
		t.Fatal(err)
	}
	if file.file != nil {
		t.Fatal("original unlinked descriptor remained open")
	}
	got, err := root.ReadFile("retired.conf")
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatal("cleanup touched a same-name substitute", err)
	}
	if err := retired.closeV1(); err != nil {
		t.Fatal("original cleanup was not idempotent", err)
	}
}

func TestRetiredCustodyConfigKeepsOriginalHandleOnChangedContents(t *testing.T) {
	root, err := cachefs.OpenPrivateRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openCustodyConfigFileV1(root, "retired.conf", []byte("original\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if file.file != nil {
			_ = file.file.Close()
		}
	}()
	if _, err := file.file.WriteAt([]byte("changed original evidence\n"), 0); err != nil {
		t.Fatal(err)
	}
	retired := &custodyRetiredConfigV1{file: file}
	if err := retired.closeV1(); err == nil {
		t.Fatal("changed original configuration was discarded")
	}
	if file.file == nil {
		t.Fatal("unconfirmed cleanup lost its original descriptor owner")
	}
	if _, err := file.file.Stat(); err != nil {
		t.Fatal("original descriptor was closed after failed cleanup", err)
	}
	if info, err := root.Lstat("retired.conf"); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("failed cleanup damaged the original evidence", err)
	}
}

func TestIncompleteCustodyCreationCleanupUsesSameInodeAndPreservesSubstitute(t *testing.T) {
	root, err := cachefs.OpenPrivateRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openCustodyConfigFileV1(root, "partial.conf", []byte("expected complete\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer file.file.Close()
	file.incomplete = true
	if err := file.file.Truncate(3); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("partial.conf", "retained-original.conf"); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("preserve replacement\n")
	if err := root.WriteFile("partial.conf", replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := file.removeCreatedV1(); err == nil {
		t.Fatal("partial creation cleanup removed a substituted name")
	}
	got, err := root.ReadFile("partial.conf")
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatal("partial cleanup touched replacement", err)
	}
	if err := root.Remove("retained-original.conf"); err != nil {
		t.Fatal(err)
	}
	if err := file.removeCreatedV1(); err != nil {
		t.Fatal("unlinked original could not settle without touching replacement", err)
	}
}

func TestPartialRuntimeCleanupFailureRetainsOriginalDescriptors(t *testing.T) {
	root, err := cachefs.OpenPrivateRoot(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openCustodyConfigFileV1(root, "partial.conf", []byte("partial\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer file.file.Close()
	runtime := &custodyRuntimeV1{root: root, service: file, pendingInstall: true}
	if err := runtime.Close(); err == nil {
		t.Fatal("unknown original manager completed partial installation cleanup")
	}
	if runtime.root != root || file.file == nil {
		t.Fatal("failed cleanup lost its actual retained root/file owner")
	}
	if _, err := file.file.Stat(); err != nil {
		t.Fatal("failed cleanup closed original descriptor", err)
	}
}
