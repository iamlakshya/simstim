package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

func runtimeReferences() ([]runtimecache.References, error) {
	manifests, err := trustedSessionManifests()
	if err != nil {
		return nil, err
	}
	refs := make([]runtimecache.References, 0, len(manifests))
	for _, manifest := range manifests {
		refs = append(refs, runtimecache.References{Label: manifest.Run.Label, Digests: manifest.Run.Artefacts})
	}
	return refs, nil
}

// trustedSessionManifests is the one host-owned retention scanner. Image
// and runtime collection share its directory and descriptor checks.
func trustedSessionManifests() ([]Manifest, error) {
	before, err := os.Lstat(paths.SessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect session runtime references: %w", err)
	}
	if !before.IsDir() || !wallCacheOwned(before) || before.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("session reference directory is unsafe")
	}
	root, err := os.OpenRoot(paths.SessionsDir())
	if err != nil {
		return nil, fmt.Errorf("open session references: %w", err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open session reference directory: %w", err)
	}
	defer dir.Close()
	current, err := dir.Stat()
	if err != nil || !os.SameFile(before, current) {
		return nil, fmt.Errorf("session reference directory changed")
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("list session runtime references: %w", err)
	}
	var manifests []Manifest
	for _, entry := range entries {
		st, err := root.Lstat(entry.Name())
		if err != nil || !st.IsDir() || !wallCacheOwned(st) || st.Mode().Perm() != 0o700 || !validID(entry.Name()) {
			return nil, fmt.Errorf("session runtime references contain an unsafe entry")
		}
		manifest, err := readRuntimeReference(root, entry.Name())
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func readRuntimeReference(root *os.Root, id string) (Manifest, error) {
	var manifest Manifest
	name := id + "/manifest.json"
	before, err := root.Lstat(name)
	if err != nil {
		return manifest, fmt.Errorf("inspect session runtime manifest: %w", err)
	}
	if !before.Mode().IsRegular() || !wallCacheOwned(before) || before.Mode().Perm() != 0o600 {
		return manifest, fmt.Errorf("session runtime manifest is unsafe")
	}
	f, err := openRuntimeReference(root, name)
	if err != nil {
		return manifest, fmt.Errorf("open session runtime manifest: %w", err)
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !runtimeReferenceSingleLink(after) {
		return manifest, fmt.Errorf("session runtime manifest changed or has another link")
	}
	dec := json.NewDecoder(f)
	if err := dec.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("read session runtime reference: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return manifest, fmt.Errorf("session runtime manifest has trailing content")
	}
	if manifest.ID != id {
		return manifest, fmt.Errorf("session runtime manifest names another session")
	}
	return manifest, nil
}
