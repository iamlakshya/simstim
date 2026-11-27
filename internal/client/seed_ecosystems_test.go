package client

import (
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/seedmeta"
)

func TestSeedEcosystemRecipeKeepsExistingPolicyReadAndExactNewSelectedTreeQuery(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, path := range []string{"." + brand.Name, "." + brand.Name + "/sandbox.toml"} {
		if !localGitRecipe([]string{"ls-tree", "-z", commit, "--", path}) {
			t.Fatal("existing exact selected policy read changed")
		}
	}
	query := append([]string{"ls-tree", "-z", "--name-only", commit, "--"}, seedmeta.RootManifests()...)
	if !localGitRecipe(query) {
		t.Fatal("exact original selected-tree metadata query refused")
	}
	for _, mutate := range []func([]string) []string{
		func(args []string) []string { args[3] = "HEAD"; return args },
		func(args []string) []string { args[3] = strings.Repeat("z", 40); return args },
		func(args []string) []string { args[5], args[6] = args[6], args[5]; return args },
		func(args []string) []string { args[5] = "*"; return args },
		func(args []string) []string { return append(args, ".env") },
		func(args []string) []string { args[2] = "-r"; return args },
	} {
		if localGitRecipe(mutate(slices.Clone(query))) {
			t.Fatal("changed ecosystem recipe widened the owned Git operation")
		}
	}
}
