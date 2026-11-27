package sandbox

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestCachePlanUsesDefaultsOnlyForLegacyAndNeverRefillsMergedEmpty(t *testing.T) {
	base := files.StatePlan{Dirs: []string{".agent/history"}, Files: []string{".agent/state.sqlite"}}
	defaults, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []*api.SandboxValues{nil, {}} {
		plan, err := CacheStatePlan(base, legacy)
		if err != nil || len(plan.Dirs) != len(defaults)+1 {
			t.Fatal("legacy record lost builtin caches", plan, err)
		}
		if !slices.Equal(plan.Files, base.Files) {
			t.Fatal("cache planning changed atomic files", plan)
		}
	}
	enabled := true
	plan, err := CacheStatePlan(base, &api.SandboxValues{KeepCaches: &enabled, Caches: []string{}})
	if err != nil || !slices.Equal(plan.Dirs, base.Dirs) {
		t.Fatal("all dropped defaults were refilled", plan, err)
	}
	selected := []string{".cache/custom"}
	plan, err = CacheStatePlan(base, &api.SandboxValues{KeepCaches: &enabled, Caches: selected})
	if err != nil || !slices.Equal(plan.Dirs, []string{".agent/history", ".cache/custom"}) {
		t.Fatal(plan, err)
	}
	plan.Dirs[0] = "changed"
	plan.Files[0] = "changed"
	if base.Dirs[0] != ".agent/history" || base.Files[0] != ".agent/state.sqlite" || selected[0] != ".cache/custom" {
		t.Fatal("per-start plan mutated protected base or merged inputs")
	}
	falseValue := false
	plan, err = CacheStatePlan(base, &api.SandboxValues{KeepCaches: &falseValue, Caches: selected})
	if err != nil || !slices.Equal(plan.Dirs, base.Dirs) {
		t.Fatal("false retained caches or changed ordinary state", plan, err)
	}
}

func TestCacheTableRefusesUnknownUnverifiedAndInstalledProgramRoots(t *testing.T) {
	valid := "[[cache]]\ntool='tool'\npath='.cache/tool'\nsource='https://example.invalid/tool/cache'\n"
	if _, err := readCaches(valid); err != nil {
		t.Fatal("valid narrow referenced data was refused", err)
	}
	for _, data := range []string{
		valid + "future = true\n",
		strings.Replace(valid, "tool='tool'", "tool=''", 1),
		strings.Replace(valid, "https://example.invalid", "http://example.invalid", 1),
		strings.Replace(valid, "https://example.invalid", "https://user:password@example.invalid", 1),
		strings.Replace(valid, ".cache/tool", ".cargo", 1),
		strings.Replace(valid, ".cache/tool", "../../outside", 1),
		valid + valid,
	} {
		if _, err := readCaches(data); err == nil {
			t.Fatal("bad built-in cache data was accepted", data)
		}
	}
}

func TestCachePlanValidatesDisabledPathsAndRefusesProgramOrStateOverlap(t *testing.T) {
	no := false
	for _, name := range []string{".cargo/bin", ".cargo", "go/bin", "go", ".local/share/pnpm", ".local/share/yarn/berry", ".yarn/plugins", ".gradle/jdks", "cache/../../outside", "cache/bin", "cache/node_modules"} {
		_, err := CacheStatePlan(files.StatePlan{}, &api.SandboxValues{KeepCaches: &no, Caches: []string{name}})
		var refusal *proto.Error
		if !errors.As(err, &refusal) || refusal.Code != "bad_value" || refusal.Message == "" || refusal.Hint == "" {
			t.Error("disabled field bypassed cache root validation or lost its hint", name, err)
		}
	}
	for _, agent := range agents.Table {
		for _, name := range agent.InstallDirs {
			if err := ValidateCachePath(name); err == nil {
				t.Error("canonical installed agent folder became cache", name)
			}
		}
	}
	yes := true
	_, err := CacheStatePlan(files.StatePlan{Files: []string{".cache/tool/state.sqlite"}}, &api.SandboxValues{KeepCaches: &yes, Caches: []string{".cache/tool"}})
	var refusal *proto.Error
	if !errors.As(err, &refusal) || refusal.Code != "bad_value" || refusal.Hint == "" {
		t.Fatal("cache parent shadowed atomic retained file or lost its refusal hint", err)
	}
}

func TestDisabledCachePlansStillOwnDirectoryAndFileSlices(t *testing.T) {
	no := false
	base := files.StatePlan{Dirs: []string{".agent/history"}, Files: []string{".agent/state.sqlite"}}
	plan, err := CacheStatePlan(base, &api.SandboxValues{KeepCaches: &no})
	if err != nil {
		t.Fatal(err)
	}
	plan.Dirs[0], plan.Files[0] = "changed", "changed"
	if base.Dirs[0] != ".agent/history" || base.Files[0] != ".agent/state.sqlite" {
		t.Fatal("disabled cache state borrowed the protected primary plan's slices")
	}
}

func TestCacheDataHasVerifiedNarrowLegacyAndXDGRoots(t *testing.T) {
	entries, err := Caches()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Cache{}
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	for _, name := range []string{".npm/_cacache", ".local/share/pnpm/store", ".yarn/berry/cache", ".local/share/yarn/berry/cache", ".cache/pip", ".cache/uv", ".cargo/registry", ".cargo/git", "go/pkg/mod", ".cache/go-build", ".gradle/caches", ".m2/repository"} {
		entry, found := byPath[name]
		if !found || entry.Source == "" {
			t.Error("documented narrow cache root missing", name)
		}
	}
	if len(byPath) != 12 {
		t.Fatal("unexpected or overlapping builtin cache set", byPath)
	}
	entries[0].Path = "changed"
	fresh, err := Caches()
	if err != nil || fresh[0].Path == "changed" {
		t.Fatal("caller changed shared data", err)
	}
}
