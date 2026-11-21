package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureCloseKeepsOriginalOwnerAfterReplacedIncoming(t *testing.T) {
	dir := t.TempDir()
	capture, err := BeginCapture(dir, StatePlan{})
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	original := capture.Incoming + "-held"
	if err := os.Rename(capture.Incoming, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(capture.Incoming, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err == nil {
		t.Fatal("a replacement was removed as the original incoming capture")
	}
	if capture.session == nil || capture.store == nil || capture.closed {
		t.Fatal("failed cleanup discarded the original metadata owner")
	}
	if err := os.Remove(capture.Incoming); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, capture.Incoming); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal("same-owner repair could not finish cleanup", err)
	}
}

func TestCaptureDescriptorCloseErrorNeverBecomesSuccess(t *testing.T) {
	capture, err := BeginCapture(t.TempDir(), StatePlan{})
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.store.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := capture.Close(); err == nil {
			t.Fatal("lost native close error became cleanup success")
		}
	}
	if capture.closed || capture.closeErr == nil {
		t.Fatal("uncertain capture cleanup was marked complete")
	}
}

func TestBeginCaptureReturnsActualPartialOwnerOnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state-files"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state-files", "CURRENT"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture, err := BeginCapture(dir, StatePlan{})
	if err == nil || capture == nil {
		t.Fatal("failed actual capture disappeared instead of returning its owner", err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
}
