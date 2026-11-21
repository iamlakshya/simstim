package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
)

type gvisorBuildItemV1 struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Arch     string `json:"arch"`
	Released string `json:"released"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}
type gvisorBuildRecordV1 struct {
	Upstream          string              `json:"upstream_commit"`
	Patch             string              `json:"patch_sha256"`
	Version           string              `json:"version"`
	Arch              string              `json:"arch"`
	Items             []gvisorBuildItemV1 `json:"items"`
	BuildInputsSHA256 string              `json:"build_inputs_sha256"`
	SBOMSHA256        string              `json:"sbom_sha256"`
}

func gvisorReleaseFailureV1(err error) error {
	return &failure{msg: "gvisor release: " + err.Error(), try: "build both native five-role closures from the exact pinned patch with scripts/build-gvisor-runtime.py, then retain and check their original build records and bytes"}
}

// The unsigned build record correlates source and outputs. Only the actual
// checked bytes become release items; their existing signed manifest supplies
// delivery pins. This grants no native suite or fixed-controller authority.
func addGvisorItemsV1(manifest *supply.Manifest, directory string, required bool) error {
	if directory == "" {
		return gvisorReleaseFailureV1(fmt.Errorf("-require-gvisor needs -gvisor <build folder>"))
	}
	if _, err := os.Stat(directory); errors.Is(err, os.ErrNotExist) && !required {
		return nil
	} else if err != nil {
		return gvisorReleaseFailureV1(err)
	}
	pins, err := readGvisorSourcePinsV1(".")
	if err != nil {
		return gvisorReleaseFailureV1(err)
	}
	// Inputs may be independently prepared, immutable native roots. Completed
	// assets always enter the existing GoReleaser/checksum output namespace.
	assets := filepath.Join("build", "gvisor", "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		return gvisorReleaseFailureV1(err)
	}
	entries, err := os.ReadDir(assets)
	if err != nil {
		return gvisorReleaseFailureV1(err)
	}
	expectedAssets := map[string]bool{}
	for _, arch := range supply.Arches {
		for _, role := range gvisorReleaseRolesV1 {
			expectedAssets[role+"-"+arch] = true
		}
		expectedAssets["gvisor-build-inputs-"+arch+".json"] = true
		expectedAssets["gvisor-build.spdx-"+arch+".json"] = true
		expectedAssets["gvisor-module-lock-"+arch+".json"] = true
	}
	for _, entry := range entries {
		if !expectedAssets[entry.Name()] || !entry.Type().IsRegular() {
			return gvisorReleaseFailureV1(fmt.Errorf("the gvisor asset folder contains an unknown entry; retain it for review"))
		}
	}
	staging, err := os.MkdirTemp(assets, ".gvisor-")
	if err != nil {
		return gvisorReleaseFailureV1(err)
	}
	defer os.RemoveAll(staging) // Only this call's exclusively created staging folder.
	var items []supply.Item
	for _, arch := range supply.Arches {
		path := filepath.Join(directory, arch, "build.json")
		raw, err := readGvisorRegularV1(path, 256<<10)
		if err != nil {
			return gvisorReleaseFailureV1(err)
		}
		record, err := decodeGvisorBuildV1(raw, pins, arch)
		if err != nil {
			return gvisorReleaseFailureV1(err)
		}
		provenance := map[string][]byte{}
		for _, extra := range []struct{ name, digest string }{{"build-inputs.json", record.BuildInputsSHA256}, {"build.spdx.json", record.SBOMSHA256}} {
			data, err := readGvisorRegularV1(filepath.Join(directory, arch, extra.name), 16<<20)
			if err != nil {
				return gvisorReleaseFailureV1(err)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != extra.digest {
				return gvisorReleaseFailureV1(fmt.Errorf("actual native build provenance differs"))
			}
			provenance[extra.name] = data
			asset := "gvisor-" + strings.TrimSuffix(extra.name, ".json") + "-" + arch + ".json"
			if err := os.WriteFile(filepath.Join(staging, asset), data, 0o444); err != nil {
				return gvisorReleaseFailureV1(err)
			}
		}
		if err := checkGvisorProvenanceV1(provenance["build-inputs.json"], provenance["build.spdx.json"], record, pins); err != nil {
			return gvisorReleaseFailureV1(err)
		}
		moduleLock, err := readGvisorRegularV1(filepath.Join(directory, arch, "MODULE.bazel.lock"), 16<<20)
		if err != nil {
			return gvisorReleaseFailureV1(err)
		}
		if err := checkGvisorModuleLockV1(provenance["build-inputs.json"], moduleLock); err != nil {
			return gvisorReleaseFailureV1(err)
		}
		if err := os.WriteFile(filepath.Join(staging, "gvisor-module-lock-"+arch+".json"), moduleLock, 0o444); err != nil {
			return gvisorReleaseFailureV1(err)
		}
		for _, original := range record.Items {
			data, err := readGvisorRegularV1(filepath.Join(directory, arch, original.Name), 256<<20)
			if err != nil {
				return gvisorReleaseFailureV1(err)
			}
			asset := original.Name + "-" + arch
			item := supply.NewItem(supply.KindGvisor, original.Name, pins.version, arch, data, supply.ReleaseFolder(manifest.Release)+"/"+asset)
			item.Released, item.Licence = pins.released, "Apache-2.0"
			item.BuildInputsSHA256, item.SBOMSHA256 = record.BuildInputsSHA256, record.SBOMSHA256
			if item.Size != original.Size || item.SHA256 != original.SHA256 {
				return gvisorReleaseFailureV1(fmt.Errorf("actual %s/%s bytes differ from the original build record", arch, original.Name))
			}
			if err := checkGvisorELFV1(data, original.Name, arch); err != nil {
				return gvisorReleaseFailureV1(err)
			}
			if err := os.WriteFile(filepath.Join(staging, asset), data, 0o555); err != nil {
				return gvisorReleaseFailureV1(err)
			}
			items = append(items, item)
		}
	}
	// Every role of BOTH native builds is checked before any completed asset
	// or manifest item is published. A partial or old build cannot fall back.
	for _, item := range items {
		asset := item.Name + "-" + item.Arch
		if err := os.Rename(filepath.Join(staging, asset), filepath.Join(assets, asset)); err != nil {
			return gvisorReleaseFailureV1(err)
		}
		if err := supply.CheckFile(item, filepath.Join(assets, asset)); err != nil {
			return gvisorReleaseFailureV1(err)
		}
	}
	for _, arch := range supply.Arches {
		for _, name := range []string{"gvisor-build-inputs-" + arch + ".json", "gvisor-build.spdx-" + arch + ".json", "gvisor-module-lock-" + arch + ".json"} {
			if err := os.Rename(filepath.Join(staging, name), filepath.Join(assets, name)); err != nil {
				return gvisorReleaseFailureV1(err)
			}
		}
	}
	manifest.Items = append(manifest.Items, items...)
	label, err := runtimeLabel(manifest)
	if err != nil {
		return gvisorReleaseFailureV1(err)
	}
	manifest.Runtime = label
	return nil
}

func decodeGvisorBuildV1(raw []byte, pins gvisorSourcePinsV1, arch string) (gvisorBuildRecordV1, error) {
	var record gvisorBuildRecordV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, err
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return record, err
	}
	if !bytes.Equal(raw, append(encoded, '\n')) {
		return record, fmt.Errorf("the gvisor build record has duplicate, trailing or noncanonical content")
	}
	if record.Upstream != pins.upstream || record.Patch != pins.patch || record.Version != pins.version || record.Arch != arch || len(record.Items) != len(gvisorReleaseRolesV1) {
		return record, fmt.Errorf("the gvisor build record differs from its current source, architecture or five-role closure")
	}
	if !supply.IsHex64(record.BuildInputsSHA256) || !supply.IsHex64(record.SBOMSHA256) {
		return record, fmt.Errorf("native gvisor provenance digests are missing")
	}
	for index, item := range record.Items {
		if item.Kind != supply.KindGvisor || item.Name != gvisorReleaseRolesV1[index] || item.Version != pins.version || item.Arch != arch || item.Released != pins.released || item.Size <= 0 || item.Size > 256<<20 || !supply.IsHex64(item.SHA256) {
			return record, fmt.Errorf("the gvisor build record has another role, pin or release at item %d", index)
		}
	}
	return record, nil
}

func checkGvisorReleaseItemsV1(manifest *supply.Manifest, directory string, required bool) error {
	present := false
	for _, item := range manifest.Items {
		present = present || item.Kind == supply.KindGvisor
	}
	if !present && !required {
		return nil
	}
	expected := &supply.Manifest{Release: manifest.Release}
	if err := addGvisorItemsV1(expected, directory, true); err != nil {
		return err
	}
	for _, want := range expected.Items {
		if got, found := manifest.Find(want.Kind, want.Name, want.Arch); !found || got != want {
			return gvisorReleaseFailureV1(fmt.Errorf("manifest %s/%s differs from the actual current native build", want.Arch, want.Name))
		}
	}
	for _, item := range manifest.Items {
		if item.Kind == supply.KindGvisor {
			if _, found := expected.Find(item.Kind, item.Name, item.Arch); !found {
				return gvisorReleaseFailureV1(fmt.Errorf("manifest contains an unknown gvisor role"))
			}
		}
	}
	label, err := runtimeLabel(manifest)
	if err != nil || manifest.Runtime != label {
		return gvisorReleaseFailureV1(fmt.Errorf("runtime label differs from the actual complete runtime items"))
	}
	return nil
}
