package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestImageVerifierSupplyCannotSubstituteMissingOrLabelledExecutables(t *testing.T) {
	manifest := &supply.Manifest{Release: "1.2.3"}
	directory := t.TempDir()
	if err := addImageVerifierItems(manifest, directory, true); err == nil {
		t.Fatal("missing offline executable became verifier support")
	}
	if err := os.WriteFile(filepath.Join(directory, "image-verifier-linux-amd64"), []byte("label without binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addImageVerifierItems(manifest, directory, false); err == nil {
		t.Fatal("a label became an offline verifier artifact")
	}
	if err := checkImageVerifierItems(manifest, true); err == nil {
		t.Fatal("partial helper metadata widened supported client platforms")
	}
}
