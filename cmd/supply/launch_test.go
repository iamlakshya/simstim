package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestLaunchSupplyRequiresActualStaticArtifactsAndExactReleaseContract(t *testing.T) {
	manifest := &supply.Manifest{Release: "1.2.3"}
	directory := t.TempDir()
	if err := addLaunchItems(manifest, directory, true); err == nil {
		t.Fatal("missing launcher artifacts became release items")
	}
	if err := os.WriteFile(filepath.Join(directory, "launch-amd64"), []byte("not an ELF executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addLaunchItems(manifest, directory, false); err == nil {
		t.Fatal("a labelled file became a static launcher artifact")
	}
	for _, arch := range supply.Arches {
		manifest.Items = append(manifest.Items, supply.Item{Kind: supply.KindLaunch, Name: "launch", Version: manifest.Release, Arch: arch, SHA256: strings.Repeat("a", 64), Size: 1, URL: supply.ReleaseFolder(manifest.Release) + "/launch-" + arch, Licence: "Apache-2.0"})
	}
	if err := checkLaunchItems(manifest, true); err != nil {
		t.Fatal("the metadata grammar rejected its exact contract", err)
	}
	manifest.Items[0].URL = supply.ReleaseFolder(manifest.Release) + "/unrelated"
	if err := checkLaunchItems(manifest, true); err == nil {
		t.Fatal("launcher metadata changed its original asset role")
	}
}
