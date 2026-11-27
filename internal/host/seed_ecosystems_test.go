package host

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/seedmeta"
)

func TestSeedEcosystemsReadOnlyTheSelectedCommittedTreeThroughExistingRunner(t *testing.T) {
	commit := strings.Repeat("a", 40)
	called := false
	run := func(directory string, args ...string) (string, error) {
		called = true
		expected := append([]string{"ls-tree", "-z", "--name-only", commit, "--"}, seedmeta.RootManifests()...)
		if directory != "trusted-stage.git" || !reflect.DeepEqual(args, expected) {
			t.Fatal("seed metadata changed its original stage, commit or fixed name query")
		}
		return "go.mod\x00package.json\x00", nil
	}
	tags, err := seedEcosystems(run, "trusted-stage.git", commit)
	if err != nil || !called || !reflect.DeepEqual(tags, []string{"go", "javascript"}) {
		t.Fatal("trusted seed ecosystem read failed", err)
	}
	called = false
	if _, err := seedEcosystems(run, "trusted-stage.git", "HEAD"); err == nil || called {
		t.Fatal("mutable ref reached the seed metadata reader")
	}
}
