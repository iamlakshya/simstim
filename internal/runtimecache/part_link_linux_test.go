//go:build linux

package runtimecache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimePartPublicationLinksActualWriterAfterIncomingReplacementV1(t *testing.T) {
	store, directory := testStore(t)
	body := []byte("original checked runtime")
	item := testManifest(body, "r1").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	defer part.Close()
	if err := part.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(directory, "incoming-retained")
	if err := os.Rename(filepath.Join(directory, "incoming"), retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "incoming"), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(directory, part.name)
	if err := os.WriteFile(foreign, []byte("another file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := part.Finish(t.Context()); err != nil {
		t.Fatal("publication selected the replacement source name", err)
	}
	installed, err := os.ReadFile(filepath.Join(directory, "sha256", item.SHA256))
	if err != nil || string(installed) != string(body) {
		t.Fatal("published bytes differ from the held original writer", err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "another file" {
		t.Fatal("publication touched the replacement parent", err)
	}
	if _, err := os.Stat(filepath.Join(retained, part.basename)); !os.IsNotExist(err) {
		t.Fatal("successful publication retained its original incoming name", err)
	}
}
