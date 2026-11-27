package cli

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestForkAndRewindCommandsUseCanonicalSharedFlags(t *testing.T) {
	root := Root()
	for name, flags := range map[string][]string{"fork": {"count", "name", "detach", "json"}, "rewind": {"yes", "json"}} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == root {
			t.Fatalf("missing command %s: %v", name, err)
		}
		for _, name := range flags {
			flag := cmd.Flags().Lookup(name)
			if flag == nil || len(flag.Annotations[sharedKey]) != 1 {
				t.Errorf("%s does not share flag %s", cmd.Name(), name)
			}
		}
	}
	if count, _ := forkCmd().Flags().GetInt("count"); count != 1 {
		t.Errorf("fork default count = %d", count)
	}
}

func TestForkAndRewindInvalidSelectionsStopBeforeHostLookup(t *testing.T) {
	for _, count := range []string{"0", "33"} {
		cmd := forkCmd()
		cmd.SetContext(context.Background())
		if err := cmd.ParseFlags([]string{"--count", count}); err != nil {
			t.Fatal(err)
		}
		var user *client.UserError
		if err := cmd.RunE(cmd, []string{"not-a-host/session"}); !errors.As(err, &user) || user.Code != "bad_count" {
			t.Errorf("invalid count reached host lookup: %v", err)
		}
	}
	for _, number := range []string{"0", "-1", "abc", "999999999999999999999999999999"} {
		cmd := rewindCmd()
		cmd.SetContext(context.Background())
		var user *client.UserError
		if err := cmd.RunE(cmd, []string{"not-a-host/session", number}); !errors.As(err, &user) || user.Code != "bad_checkpoint" {
			t.Errorf("invalid checkpoint %q reached host lookup: %v", number, err)
		}
	}
}

func TestRewindPromptNeedsAnswerWithoutInputAndPicksExactNumber(t *testing.T) {
	p := &prompter{ctx: context.Background(), on: false, path: "tool rewind", line: []string{"rewind", "box/parent23"}}
	var user *client.UserError
	if _, err := p.confirm(qRewind, "Restore?", "a yes"); !errors.As(err, &user) || user.Kind != client.KindNeedsAnswer || !strings.Contains(user.Hint, "-y") {
		t.Fatalf("input-off confirmation = %v", err)
	}
	p = testPrompts("4\n2\n", &bytes.Buffer{})
	if number, err := askCheckpointNumber(p, 3); err != nil || number != 2 {
		t.Fatalf("checkpoint choice = %d, %v", number, err)
	}
	p = testPrompts("0\n4\nbad\n", &bytes.Buffer{})
	if _, err := askCheckpointNumber(p, 3); !isCancel(err) {
		t.Fatalf("three invalid selections = %v", err)
	}
}

func TestRewindReviewIncludesChangedAddedAndRemovedBranches(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ref := func(name, oid string) proto.BundleRef { return proto.BundleRef{Name: "refs/heads/" + name, OID: oid} }
	got := checkpointChanges([]proto.BundleRef{ref("same", a), ref("changed", a), ref("removed", a)}, []proto.BundleRef{ref("added", b), ref("same", a), ref("changed", b)})
	want := []string{"added: " + b + " -> (absent)", "changed: " + b + " -> " + a, "removed: (absent) -> " + a}
	if !slices.Equal(got, want) {
		t.Fatalf("review = %q, want %q", got, want)
	}
	cmd := rewindCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	when := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	printCheckpoints(cmd, api.CheckpointsResult{Checkpoints: []api.CheckpointInfo{{Name: when.Format(api.CheckpointTimeLayout), CreatedAt: when}}})
	if output.String() != "1  2026-02-03T04:05:06Z\n" {
		t.Fatalf("newest checkpoint listing = %q", output.String())
	}
}
