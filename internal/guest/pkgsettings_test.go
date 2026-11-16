package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func packageSettingsPlan(mode, age string) *proto.PackageManagersV1 {
	return &proto.PackageManagersV1{Version: 1, Mode: mode, MinReleaseAge: age, StartedAt: "2026-11-15T12:00:00Z"}
}

func TestGoldenPackageManagerSettingsForEveryLevelAndReleaseAge(t *testing.T) {
	type snapshot struct {
		Mode        string                  `json:"mode"`
		Age         string                  `json:"age"`
		Environment []string                `json:"environment"`
		Files       []proto.PackageHomeFile `json:"files"`
	}
	var snapshots []snapshot
	for _, mode := range []string{"guarded", "strict", "off"} {
		for _, age := range []string{"0d", "1d", "7d"} {
			defaults, files, err := proto.RenderPackageManagerSettings(packageSettingsPlan(mode, age))
			if err != nil {
				t.Fatal(mode, age, err)
			}
			item := snapshot{Mode: mode, Age: age, Environment: []string{}, Files: append([]proto.PackageHomeFile{}, files...)}
			for _, setting := range defaults {
				item.Environment = append(item.Environment, setting.Name+"="+setting.Value)
			}
			snapshots = append(snapshots, item)
		}
	}
	actual, err := json.MarshalIndent(snapshots, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	expected, err := os.ReadFile("testdata/pkgsettings.golden")
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("package settings differ from the reviewed 0d/1d/7d fixtures: %v\n%s", err, actual)
	}
}

func TestPackageManagerEnvironmentKeepsOriginalUserNamesAndExplicitEmptyValues(t *testing.T) {
	input := []string{"PATH=/usr/bin:/bin", "npm_config_ignore_scripts=false", "NPM_CONFIG_MIN_RELEASE_AGE=", "GOTOOLCHAIN=auto", "PIP_ONLY_BINARY=", "UV_NO_BUILD=0", "GOFLAGS=-mod=mod", "GOPRIVATE=corp.invalid/*"}
	before := slices.Clone(input)
	output, err := PackageManagerEnvironment(packageSettingsPlan("strict", "7d"), input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(input, before) {
		t.Fatal("package defaults changed the original ordinary environment")
	}
	for _, value := range input {
		if !slices.Contains(output, value) {
			t.Fatalf("the original user entry lost precedence: %s", strings.SplitN(value, "=", 2)[0])
		}
	}
	for _, forbidden := range []string{"npm_config_ignore_scripts=true", "npm_config_min_release_age=7", "GOTOOLCHAIN=local", "PIP_ONLY_BINARY=:all:", "UV_NO_BUILD=1", "GOFLAGS=-mod=readonly"} {
		if slices.Contains(output, forbidden) {
			t.Fatalf("a package default replaced a user value: %s", strings.SplitN(forbidden, "=", 2)[0])
		}
	}
	output[len(output)-1] = "caller mutation"
	if !slices.Equal(input, before) {
		t.Fatal("the derived environment borrowed the original slice")
	}
}

func TestPackageSettingsNeverImportAmbientGoValuesOrFreezeZeroDayAge(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=readonly")
	t.Setenv("GOPRIVATE", "ambient.invalid/*")
	environment, err := PackageManagerEnvironment(packageSettingsPlan("strict", "0d"), []string{"HOME=/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		for _, forbidden := range []string{"GOFLAGS", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOINSECURE", "npm_config_min_release_age", "YARN_NPM_MINIMAL_AGE_GATE", "PIP_UPLOADED_PRIOR_TO", "UV_EXCLUDE_NEWER"} {
			if name == forbidden {
				t.Fatalf("zero-age or ambient setting was injected: %s", name)
			}
		}
	}
	_, files, err := proto.RenderPackageManagerSettings(packageSettingsPlan("guarded", "0d"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.Contains(file.Content, "minimumReleaseAge") || strings.Contains(file.Content, "2026-") {
			t.Fatal("zero days became a frozen age cutoff")
		}
	}
}

func TestPackageSettingsOffAndLegacyPlansOwnTheirUnchangedEnvironment(t *testing.T) {
	input := []string{"GOTOOLCHAIN=custom", "KEEP="}
	for _, plan := range []*proto.PackageManagersV1{nil, packageSettingsPlan("off", "7d")} {
		output, err := PackageManagerEnvironment(plan, input)
		if err != nil || !slices.Equal(output, input) {
			t.Fatal("disabled package settings changed the environment", err)
		}
		output[0] = "changed"
		if input[0] != "GOTOOLCHAIN=custom" {
			t.Fatal("disabled package settings borrowed caller storage")
		}
	}
}

func packageSettingsRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, directory
}

func TestPackageSettingsWriterCreatesCanonicalHomeAndXDGFilesOnly(t *testing.T) {
	root, _ := packageSettingsRoot(t)
	plan := packageSettingsPlan("guarded", "7d")
	if err := writePackageManagerFiles(t.Context(), root, plan); err != nil {
		t.Fatal(err)
	}
	_, files, err := proto.RenderPackageManagerSettings(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		content, err := root.ReadFile(file.Path)
		if err != nil || string(content) != file.Content {
			t.Fatal("canonical package settings file differs", file.Path, err)
		}
		info, err := root.Lstat(file.Path)
		if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("package settings file type/mode differs", file.Path, err)
		}
	}
	if _, err := root.Lstat(".cargo"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("package settings invented a cargo configuration")
	}
}

func TestPackageSettingsRefuseForeignFilesAndParentLinksBeforeAnyWrite(t *testing.T) {
	for _, kind := range []string{"file", "link"} {
		t.Run(kind, func(t *testing.T) {
			root, directory := packageSettingsRoot(t)
			if kind == "file" {
				if err := root.WriteFile(".bunfig.toml", []byte("foreign user settings"), 0644); err != nil {
					t.Fatal(err)
				}
			} else {
				outside := t.TempDir()
				if err := os.Symlink(outside, filepath.Join(directory, ".config")); err != nil {
					t.Skipf("native link fixture unavailable: %v", err)
				}
			}
			if err := writePackageManagerFiles(t.Context(), root, packageSettingsPlan("guarded", "7d")); err == nil {
				t.Fatal("a foreign settings name accepted replacement or traversal")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 {
				t.Fatal("preflight refusal still created another destination", err)
			}
			if kind == "file" {
				content, err := root.ReadFile(".bunfig.toml")
				if err != nil || string(content) != "foreign user settings" {
					t.Fatal("package settings cleanup changed a foreign file", err)
				}
			}
		})
	}
}

func TestPackageSettingsOffMalformedAndCancelledPlansWriteNothing(t *testing.T) {
	for _, mode := range []string{"off", "future", "age", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			root, directory := packageSettingsRoot(t)
			plan := packageSettingsPlan("guarded", "7d")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "off":
				plan.Mode = "off"
			case "future":
				plan.Version++
			case "age":
				plan.MinReleaseAge = "90m"
			case "cancelled":
				cancel()
			}
			err := writePackageManagerFiles(ctx, root, plan)
			if mode == "off" && err != nil || mode != "off" && err == nil {
				t.Fatal("package settings plan admission differs", mode, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("rejected or disabled plan wrote a home file", err)
			}
		})
	}
}

func TestPackageSettingsWireRejectsCallerPathsEnvAndTrailingPayload(t *testing.T) {
	raw, err := json.Marshal(packageSettingsPlan("guarded", "7d"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePackageManagerPlan(raw)
	if err != nil || !reflect.DeepEqual(decoded, packageSettingsPlan("guarded", "7d")) {
		t.Fatal("the original closed settings plan did not decode", err)
	}
	for _, data := range []string{string(raw) + "{}", strings.Replace(string(raw), `"v":1`, `"v":1,"path":"/outside"`, 1), strings.Replace(string(raw), `"v":1`, `"v":1,"env":["PAYLOAD=foreign"]`, 1), strings.Replace(string(raw), `"mode":"guarded"`, `"mode":"guarded","Mode":"off"`, 1)} {
		if _, err := decodePackageManagerPlan([]byte(data)); err == nil {
			t.Fatal("a caller path, environment, alias or trailing payload was accepted")
		}
	}
}
