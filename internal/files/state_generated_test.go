package files

import (
	"reflect"
	"slices"
	"testing"
)

func TestGeneratedHomeExclusionDropsOnlyExactFilesAndOwnsTheResult(t *testing.T) {
	plan := StatePlan{Dirs: []string{".agent/sessions"}, Files: []string{".aws/credentials", ".bunfig.toml", ".agent/state", ".aws/credentials.backup"}}
	before := StatePlan{Dirs: slices.Clone(plan.Dirs), Files: slices.Clone(plan.Files)}
	paths := []string{".aws/credentials", ".git-credentials", ".bunfig.toml", ".config/.bunfig.toml", ".config/pnpm/config.yaml"}
	result, err := ExcludeGeneratedHomeState(plan, paths)
	if err != nil {
		t.Fatal(err)
	}
	want := StatePlan{Dirs: []string{".agent/sessions"}, Files: []string{".agent/state", ".aws/credentials.backup"}}
	if !reflect.DeepEqual(result, want) || !reflect.DeepEqual(plan, before) {
		t.Fatalf("generated path exclusion changed unrelated state: %+v / %+v", result, plan)
	}
	result.Dirs[0], result.Files[0] = "caller mutation", "caller mutation"
	if !reflect.DeepEqual(plan, before) {
		t.Fatal("the restricted state plan borrowed the original slices")
	}
}

func TestGeneratedHomeExclusionRefusesEveryDirectoryAndPathOverlap(t *testing.T) {
	for _, test := range []struct {
		name string
		plan StatePlan
	}{
		{name: "directory ancestor", plan: StatePlan{Dirs: []string{".aws"}}},
		{name: "directory exact", plan: StatePlan{Dirs: []string{".aws/credentials"}}},
		{name: "directory descendant", plan: StatePlan{Dirs: []string{".aws/credentials/nested"}}},
		{name: "file ancestor", plan: StatePlan{Files: []string{".aws"}}},
		{name: "file descendant", plan: StatePlan{Files: []string{".aws/credentials/nested"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ExcludeGeneratedHomeState(test.plan, []string{".aws/credentials"}); err == nil {
				t.Fatal("an overlapping bind or restore plan was silently narrowed")
			}
		})
	}
}

func TestGeneratedHomeExclusionValidatesOriginalPlanBeforeDroppingFiles(t *testing.T) {
	for _, plan := range []StatePlan{
		{Files: []string{".config/pnpm/config.yaml"}},
		{Files: []string{".aws/credentials", ".aws/credentials"}},
		{Dirs: []string{".aws"}, Files: []string{".aws/credentials"}},
	} {
		if _, err := ExcludeGeneratedHomeState(plan, []string{".aws/credentials", ".config/pnpm/config.yaml"}); err == nil {
			t.Fatal("generated-file exclusion laundered an invalid state plan")
		}
	}
}

func TestGeneratedHomeExclusionRefusesUnsafeOrRepeatedCanonicalPaths(t *testing.T) {
	for _, paths := range [][]string{{""}, {"."}, {"../outside"}, {"/absolute"}, {"one/../outside"}, {"one//two"}, {".git/config"}, {".aws/credentials", ".aws/credentials"}} {
		if _, err := ExcludeGeneratedHomeState(StatePlan{}, paths); err == nil {
			t.Fatalf("unsafe generated paths accepted: %q", paths)
		}
	}
	if result, err := ExcludeGeneratedHomeState(StatePlan{}, nil); err != nil || result.Dirs != nil || result.Files != nil {
		t.Fatal("the empty restriction changed an empty plan", err)
	}
}
