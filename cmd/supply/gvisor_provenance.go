package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/supply"
)

type gvisorBuildInputsV1 struct {
	Version  int                              `json:"version"`
	Upstream string                           `json:"upstream_commit"`
	Scope    string                           `json:"tool_scope"`
	Bazel    struct{ Version, SHA256 string } `json:"bazel"`
	Go       struct {
		Version, SHA256, Selection string
		Patch                      string `json:"patch_sha256"`
	} `json:"go_sdk"`
	RulesGo      string              `json:"rules_go"`
	Gazelle      string              `json:"gazelle"`
	Source       map[string]string   `json:"source_files"`
	ResolvedLock string              `json:"resolved_module_lock_sha256"`
	RuntimePatch string              `json:"runtime_patch_sha256"`
	SourceMap    string              `json:"runtime_source_map_sha256"`
	Items        []gvisorBuildItemV1 `json:"items"`
	Epoch        int64               `json:"source_epoch"`
}

type gvisorBuildToolPinsV1 struct {
	Version   int               `json:"version"`
	Upstream  string            `json:"upstream_commit"`
	Bazel     string            `json:"bazel_version"`
	Go        string            `json:"go_sdk_version"`
	RulesGo   string            `json:"rules_go"`
	Gazelle   string            `json:"gazelle"`
	Patch     string            `json:"sdk_patch"`
	Source    map[string]string `json:"source_files"`
	Artifacts map[string]struct {
		Bazel struct {
			SHA string `json:"sha256"`
		} `json:"bazel"`
		Go struct {
			SHA string `json:"sha256"`
		} `json:"go_sdk"`
	} `json:"artifacts"`
}

func gvisorDigestV1(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func checkGvisorProvenanceV1(inputs, sbom []byte, record gvisorBuildRecordV1, pins gvisorSourcePinsV1) error {
	var build gvisorBuildInputsV1
	if err := json.Unmarshal(inputs, &build); err != nil {
		return err
	}
	lockData, err := readGvisorRegularV1("third_party/gvisor/build-tools.json", 64<<10)
	if err != nil {
		return err
	}
	var lock gvisorBuildToolPinsV1
	if err := json.Unmarshal(lockData, &lock); err != nil {
		return err
	}
	platform, found := lock.Artifacts["linux/"+record.Arch]
	sourceMap, err := readGvisorRegularV1("third_party/gvisor/hostguard-inputs.json", 4<<20)
	if err != nil {
		return err
	}
	if !found || lock.Version != 1 || lock.Upstream != pins.upstream || !supply.IsHex64(platform.Bazel.SHA) || !supply.IsHex64(platform.Go.SHA) ||
		build.Version != 1 || build.Upstream != pins.upstream || build.Scope != "upstream-gvisor-only" || build.RuntimePatch != pins.patch ||
		build.Bazel.Version != lock.Bazel || build.Bazel.SHA256 != platform.Bazel.SHA || build.Go.Version != lock.Go || build.Go.SHA256 != platform.Go.SHA ||
		build.Go.Selection != "rules_go.from_file" || build.RulesGo != lock.RulesGo || build.Gazelle != lock.Gazelle ||
		!supply.IsHex64(build.ResolvedLock) || build.SourceMap != gvisorDigestV1(sourceMap) || build.Epoch <= 0 || len(build.Items) != len(record.Items) {
		return fmt.Errorf("native build provenance differs from its actual selected source/tools")
	}
	if len(lock.Source) != 4 || len(build.Source) != len(lock.Source)+1 || build.Go.Patch != lock.Source[lock.Patch] || !supply.IsHex64(build.Go.Patch) {
		return fmt.Errorf("native build SDK patch or source input closure differs")
	}
	for name, expected := range lock.Source {
		if !supply.IsHex64(expected) || build.Source[name] != expected {
			return fmt.Errorf("native build upstream source input differs: %s", name)
		}
	}
	if !supply.IsHex64(build.Source["images/default/bazelversion"]) {
		return fmt.Errorf("native build version source is missing")
	}
	for index := range record.Items {
		if record.Items[index] != build.Items[index] {
			return fmt.Errorf("native build provenance names different output bytes")
		}
	}
	return checkGvisorSBOMV1(inputs, sbom, record)
}

func checkGvisorSBOMV1(inputs, sbom []byte, record gvisorBuildRecordV1) error {
	var document struct {
		Version  string `json:"spdxVersion"`
		Name     string `json:"name"`
		Packages []struct {
			Name      string                                      `json:"name"`
			Version   string                                      `json:"versionInfo"`
			Checksums []struct{ Algorithm, ChecksumValue string } `json:"checksums"`
		} `json:"packages"`
		Annotations []struct {
			Comment string `json:"comment"`
		} `json:"annotations"`
	}
	if err := json.Unmarshal(sbom, &document); err != nil {
		return err
	}
	if document.Version != "SPDX-2.3" || document.Name != "gvisor-native-"+record.Version+"-"+record.Arch || len(document.Packages) != len(record.Items) ||
		len(document.Annotations) != 1 || document.Annotations[0].Comment != "Reproducible source epoch; observed build inputs follow.\n"+string(inputs) {
		return fmt.Errorf("native build SBOM has another output or input closure")
	}
	for index, item := range record.Items {
		pkg := document.Packages[index]
		if pkg.Name != item.Name || pkg.Version != record.Version || len(pkg.Checksums) != 1 || pkg.Checksums[0].Algorithm != "SHA256" || pkg.Checksums[0].ChecksumValue != item.SHA256 {
			return fmt.Errorf("native build SBOM names different output bytes")
		}
	}
	return nil
}

func checkGvisorModuleLockV1(inputs, moduleLock []byte) error {
	var build gvisorBuildInputsV1
	if json.Unmarshal(inputs, &build) != nil || !json.Valid(moduleLock) || build.ResolvedLock != gvisorDigestV1(moduleLock) {
		return fmt.Errorf("native resolved module lock differs from its retained build input")
	}
	return nil
}

func checkGvisorUploadedProvenanceV1(manifest *supply.Manifest, downloads string) error {
	checked := map[string]supply.Item{}
	for _, item := range manifest.Items {
		if item.Kind != supply.KindGvisor {
			continue
		}
		if previous, found := checked[item.Arch]; found {
			if previous.BuildInputsSHA256 != item.BuildInputsSHA256 || previous.SBOMSHA256 != item.SBOMSHA256 {
				return gvisorReleaseFailureV1(fmt.Errorf("signed native companions have different provenance"))
			}
			continue
		}
		checked[item.Arch] = item
		inputs, err := readGvisorRegularV1(filepath.Join(downloads, "gvisor-build-inputs-"+item.Arch+".json"), 16<<20)
		if err != nil {
			return gvisorReleaseFailureV1(err)
		}
		sbom, err := readGvisorRegularV1(filepath.Join(downloads, "gvisor-build.spdx-"+item.Arch+".json"), 16<<20)
		if err != nil {
			return gvisorReleaseFailureV1(err)
		}
		moduleLock, err := readGvisorRegularV1(filepath.Join(downloads, "gvisor-module-lock-"+item.Arch+".json"), 16<<20)
		if err != nil {
			return gvisorReleaseFailureV1(err)
		}
		if gvisorDigestV1(inputs) != item.BuildInputsSHA256 || gvisorDigestV1(sbom) != item.SBOMSHA256 {
			return gvisorReleaseFailureV1(fmt.Errorf("uploaded native provenance differs from its signed manifest digest"))
		}
		if err := checkGvisorModuleLockV1(inputs, moduleLock); err != nil {
			return gvisorReleaseFailureV1(err)
		}
	}
	return nil
}
