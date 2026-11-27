package pullexport

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

func TestGuestRepositorySelectionOwnsProtectedPlanAndRequiresExplicitChoice(t *testing.T) {
	options := GuestOptions{ID: strings.Repeat("a", 26), Primary: "primary2", Cwd: "/work/first", PullMax: 1 << 20,
		Repositories: []GuestRepository{{Name: "first", Cwd: "/work/first", Seeds: []string{strings.Repeat("a", 40)}}, {Name: "second", Cwd: "/work/second", Seeds: []string{strings.Repeat("b", 40)}}},
		Call:         func(context.Context, string, any, any) error { return nil }, Open: func(context.Context, link.Kind, any) (*link.Stream, error) { return nil, nil }, Record: func(context.Context, []string) error { return nil }, Close: func() error { return nil }}
	source, err := NewGuestSource(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Repositories[1].Cwd = "/work/foreign"
	options.Repositories[1].Seeds[0] = strings.Repeat("c", 40)
	if err := source.SelectRepository(""); err == nil {
		t.Fatal("two protected repositories silently chose the first")
	}
	if err := source.SelectRepository("../second"); err == nil {
		t.Fatal("repository selector accepted a path")
	}
	if err := source.SelectRepository("second"); err != nil {
		t.Fatal(err)
	}
	if source.options.Cwd != "/work/second" || len(source.SeedHeads()) != 1 || source.SeedHeads()[0] != strings.Repeat("b", 40) {
		t.Fatal("selection used changed input or another repository's seed")
	}
	if err := source.SelectRepository("first"); err == nil {
		t.Fatal("the retained source changed repositories")
	}
}

type repositoryFixture struct {
	*exportFixture
	selected string
}

func (f *repositoryFixture) SelectRepository(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selected = name
	return nil
}

func TestRepositoryCorrelationSurvivesManagerCaptureAndRefusesChangedRetry(t *testing.T) {
	fixture := &repositoryFixture{exportFixture: &exportFixture{closed: make(chan struct{})}}
	directory := t.TempDir()
	manager, err := New(t.Context(), Options{Directory: func(string) (string, error) { return directory, nil }, Open: func(context.Context, string, string) (Source, error) { return fixture, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	first, err := manager.Begin(t.Context(), "owner", "primary2", api.PullBeginArgs{ID: "fixture2", Token: strings.Repeat("a", 26), Repo: "second"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if first.Repo != "second" || fixture.selected != "second" {
		t.Fatalf("repository correlation changed: %+v", first)
	}
	if _, err := manager.Read(t.Context(), "owner", api.PullReadArgs{ID: first.ID, Token: first.Token}); err != nil {
		t.Fatal(err)
	}
	_, err = manager.PrepareBegin(t.Context(), "owner", "primary2", api.PullBeginArgs{ID: first.ID, Token: strings.Repeat("b", 26), Previous: first.Token, Full: true, Repo: "first"}, 1<<20)
	if err == nil {
		t.Fatal("full retry changed repository on the retained primary owner")
	}
	fixture.mu.Lock()
	count := len(fixture.captures)
	fixture.mu.Unlock()
	if count != 1 {
		t.Fatalf("rejected retry captured again: %d", count)
	}
}
