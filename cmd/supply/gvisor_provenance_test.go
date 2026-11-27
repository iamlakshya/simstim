package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func writeGvisorFixtureJSONV1(t *testing.T, name string, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func writeGvisorFixtureToolPinsV1(t *testing.T, repository string, pins gvisorSourcePinsV1) {
	t.Helper()
	lock := gvisorBuildToolPinsV1{Version: 1, Upstream: pins.upstream, Bazel: "8.8.1", Go: "1.27.3", RulesGo: "0.63.0", Gazelle: "0.51.3", Patch: "tools/go_types_memoize.patch", Source: map[string]string{}}
	for _, name := range []string{"MODULE.bazel", "go.mod", ".bazelrc", lock.Patch} {
		lock.Source[name] = gvisorDigestV1([]byte(name))
	}
	// Marshal the portable fixture through a map so the test need not copy
	// the private anonymous per-platform struct type.
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	platforms := map[string]any{}
	for _, arch := range supply.Arches {
		platforms["linux/"+arch] = map[string]any{"bazel": map[string]string{"sha256": strings.Repeat("b", 64)}, "go_sdk": map[string]string{"sha256": strings.Repeat("c", 64)}}
	}
	object["artifacts"] = platforms
	writeGvisorFixtureJSONV1(t, filepath.Join(repository, "third_party/gvisor/build-tools.json"), object)
}

func writeGvisorFixtureProvenanceV1(t *testing.T, repository, directory string, record *gvisorBuildRecordV1) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repository, "third_party/gvisor/build-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock gvisorBuildToolPinsV1
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	sourceMap, err := os.ReadFile(filepath.Join(repository, "third_party/gvisor/hostguard-inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	build := gvisorBuildInputsV1{Version: 1, Upstream: record.Upstream, Scope: "upstream-gvisor-only", RulesGo: lock.RulesGo, Gazelle: lock.Gazelle, Source: map[string]string{}, RuntimePatch: record.Patch, SourceMap: gvisorDigestV1(sourceMap), Items: record.Items, Epoch: 1}
	build.Bazel.Version, build.Bazel.SHA256 = lock.Bazel, lock.Artifacts["linux/"+record.Arch].Bazel.SHA
	build.Go.Version, build.Go.SHA256, build.Go.Selection, build.Go.Patch = lock.Go, lock.Artifacts["linux/"+record.Arch].Go.SHA, "rules_go.from_file", lock.Source[lock.Patch]
	for name, digest := range lock.Source {
		build.Source[name] = digest
	}
	build.Source["images/default/bazelversion"] = gvisorDigestV1([]byte("8.8.1\n"))
	moduleLock := writeGvisorFixtureJSONV1(t, filepath.Join(directory, "MODULE.bazel.lock"), map[string]int{"lockFileVersion": 1})
	build.ResolvedLock = gvisorDigestV1(moduleLock)
	inputs := writeGvisorFixtureJSONV1(t, filepath.Join(directory, "build-inputs.json"), build)
	packages := []map[string]any{}
	for _, item := range record.Items {
		packages = append(packages, map[string]any{"name": item.Name, "versionInfo": record.Version, "checksums": []map[string]string{{"algorithm": "SHA256", "checksumValue": item.SHA256}}})
	}
	sbom := writeGvisorFixtureJSONV1(t, filepath.Join(directory, "build.spdx.json"), map[string]any{"spdxVersion": "SPDX-2.3", "name": "gvisor-native-" + record.Version + "-" + record.Arch, "packages": packages, "annotations": []map[string]string{{"comment": "Reproducible source epoch; observed build inputs follow.\n" + string(inputs)}}})
	record.BuildInputsSHA256, record.SBOMSHA256 = gvisorDigestV1(inputs), gvisorDigestV1(sbom)
}

func TestGvisorProvenanceRequiresActualToolsSDKPatchAndRetainedLockV1(t *testing.T) {
	for _, change := range []string{"sdk-version", "sdk-patch", "module-source", "module-lock", "sbom", "input-bytes"} {
		t.Run(change, func(t *testing.T) {
			repository, input, _ := gvisorReleaseFixtureV1(t)
			t.Chdir(repository)
			directory := filepath.Join(input, "amd64")
			data, err := os.ReadFile(filepath.Join(directory, "build.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record gvisorBuildRecordV1
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			inputs, err := os.ReadFile(filepath.Join(directory, "build-inputs.json"))
			if err != nil {
				t.Fatal(err)
			}
			var build gvisorBuildInputsV1
			if err := json.Unmarshal(inputs, &build); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "sdk-version":
				build.Go.Version = "1.26.11"
			case "sdk-patch":
				build.Go.Patch = strings.Repeat("e", 64)
			case "module-source":
				build.Source["MODULE.bazel"] = strings.Repeat("e", 64)
			case "module-lock":
				writeGvisorFixtureJSONV1(t, filepath.Join(directory, "MODULE.bazel.lock"), map[string]int{"lockFileVersion": 2})
			case "sbom":
				writeGvisorFixtureJSONV1(t, filepath.Join(directory, "build.spdx.json"), map[string]string{"spdxVersion": "SPDX-2.3"})
			case "input-bytes":
				inputs = append(inputs, ' ')
			}
			if change == "sdk-version" || change == "sdk-patch" || change == "module-source" {
				inputs = writeGvisorFixtureJSONV1(t, filepath.Join(directory, "build-inputs.json"), build)
				record.BuildInputsSHA256 = gvisorDigestV1(inputs) // unsigned record is no authority
				writeGvisorRecordV1(t, filepath.Join(directory, "build.json"), record)
			} else if change == "input-bytes" {
				if err := os.WriteFile(filepath.Join(directory, "build-inputs.json"), inputs, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manifest := &supply.Manifest{Release: "1.2.3"}
			if err := addGvisorItemsV1(manifest, input, true); err == nil || len(manifest.Items) != 0 {
				t.Fatal("changed tool or provenance published a native closure", err)
			}
		})
	}
}

func TestGvisorUploadedProvenanceBindsSignedDigestsAndGeneratedLockV1(t *testing.T) {
	repository, input, _ := gvisorReleaseFixtureV1(t)
	t.Chdir(repository)
	manifest := &supply.Manifest{Release: "1.2.3"}
	if err := addGvisorItemsV1(manifest, input, true); err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(repository, "build/gvisor/assets")
	if err := checkGvisorUploadedProvenanceV1(manifest, assets); err != nil {
		t.Fatal(err)
	}
	lockAsset := filepath.Join(assets, "gvisor-module-lock-arm64.json")
	if err := os.Chmod(lockAsset, 0o600); err != nil {
		t.Fatal(err)
	}
	writeGvisorFixtureJSONV1(t, lockAsset, map[string]int{"lockFileVersion": 2})
	if err := checkGvisorUploadedProvenanceV1(manifest, assets); err == nil {
		t.Fatal("another module lock passed signed input binding")
	}
}
