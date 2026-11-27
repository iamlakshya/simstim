package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// These fixtures exercise the actual client Source/grant/producer machinery
// with a descriptive host peer. They prove no native host or VMM acceptance.
func rewindClientFixtureV1(t *testing.T, changeCheck bool, mutate ...func(*api.RewindResult)) (*Conn, *GrantRequest, context.Context, api.RewindArgs, string, *int) {
	t.Helper()
	h := h5GrantState(t)
	h.WallFloor, h.Secrets = "microvm", []string{"API_KEY"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "value-order")
	settings := "[sandbox]\nssh_keys=['DEPLOY_KEY']\n[secrets.API_KEY]\nrun=" + quotedCommand("printf token; printf A >> "+ShellWord(marker)) + "\n[secrets.DEPLOY_KEY]\nrun=" + quotedCommand("printf synthetic-key; printf K >> "+ShellWord(marker)) + "\nkind='ssh-key'\non_hosts=['box']\n"
	writeRetainedImagePolicyFixture(t, settings)
	parent := h5Policy()
	parent.request = strings.Repeat("9", 32)
	parent.Ceiling, parent.CreationWall, parent.Exposed = TierOwn, "microvm", nil
	parent.Exposed = []string{"API_KEY"}
	parent.DeliveryIntent = &api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"API_KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}
	creation, releaseCreation, err := LockHost(t.Context(), h.Name, "rewind fixture's original creation metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err := commitSandboxBoundSchema(creation, h); err != nil {
		releaseCreation()
		t.Fatal(err)
	}
	if err := reserveSandbox(creation, h, parent); err != nil {
		releaseCreation()
		t.Fatal(err)
	}
	stored, err := LoadSandboxPolicy(creation, h, parent.SessionID)
	if err != nil {
		releaseCreation()
		t.Fatal(err)
	}
	child := cloneSandboxPolicy(stored)
	child.SessionID, child.request = "child234", strings.Repeat("8", 32)
	child.lineage = &sandboxLineage{Version: 1, Parent: parent.SessionID, Root: parent.SessionID, Allowed: []string{"API_KEY"}}
	if err := reserveSandbox(creation, h, *child); err != nil {
		releaseCreation()
		t.Fatal(err)
	}
	releaseCreation()
	features := []string{"resume", "secrets", "session.id", "policy", PolicyLayerFeature, api.FeatureRewind, api.FeatureRewindPreparationV1, api.FeatureSpawn, api.FeatureSandboxSnapshot, api.FeatureResumePreparationV1, api.FeatureNamedSSHKeysV1, api.FeatureSandboxCohort, api.FeatureBoundSecrets}
	c := finishGeneratedTransportFixture(t, h, parent.HostID, features...)
	c.pending = map[uint64]chan proto.Response{}
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	selection := api.RewindArgs{ID: child.SessionID, Checkpoint: "20260203T040506.000Z"}
	snapshot := sharedGrantSnapshot(parent.SessionID, child.SessionID)
	var prepared api.RewindPrepareResultV1
	commits := new(int)
	sharedGrantWire(t, c, func(request proto.Request) any {
		switch request.Op {
		case "policy.ls":
			return api.Policy{}
		case "ls":
			return []Session{{ID: parent.SessionID, Wall: &api.WallInfo{Level: "microvm"}, Secrets: []string{"API_KEY"}}, {ID: child.SessionID, SandboxOf: parent.SessionID, Wall: &api.WallInfo{Level: "microvm"}, Secrets: []string{"API_KEY"}}}
		case "sandbox.snapshot":
			var args api.SandboxSnapshotArgs
			if json.Unmarshal(request.Args, &args) != nil {
				t.Fatal("invalid snapshot fixture request")
			}
			if args.Cohort {
				view := &api.SandboxCohortView{Snapshot: snapshot.Clone(), RunGeneration: strings.Repeat("b", 32), SecretState: rewindFixturePublicStateV3("c")}
				if *commits != 0 {
					view.Snapshot.Generation, view.RunGeneration, view.SecretState = strings.Repeat("f", 64), strings.Repeat("e", 32), rewindFixturePublicStateV3("d")
				}
				return view
			}
			return snapshot
		case api.OpRewindPrepareV1:
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("value command preceded the original rewind preparation")
			}
			var args api.RewindPrepareArgsV1
			if json.Unmarshal(request.Args, &args) != nil || args.Validate() != nil || args.Selection != selection || args.Start.Prepared != nil {
				t.Fatal("invalid or substituted rewind prepare")
			}
			digest, err := api.RewindPreparationDigestV1(args.Selection, args.Start, args.Floor)
			if err != nil {
				t.Fatal(err)
			}
			prepared = api.RewindPrepareResultV1{V: 1, Prepared: &api.PreparedRewindV1{V: 1, Token: strings.Repeat("c", 64)}, Selection: selection, Source: snapshot.Clone(), StartDigest: digest}
			return prepared
		case api.OpRewindPreparedCheckV1:
			if changeCheck {
				changed := prepared
				changed.StartDigest = strings.Repeat("d", 64)
				return changed
			}
			return prepared
		case api.OpRewindCommitV1:
			*commits = *commits + 1
			var args api.RewindCommitArgsV1
			if json.Unmarshal(request.Args, &args) != nil || args.Validate() != nil || args.Selection != selection || args.Start.Prepared != nil || args.Start.Secrets["API_KEY"] != "token" || len(args.Start.Members) != 1 || args.Start.Members[0].ID != child.SessionID || args.Start.Members[0].Secrets["API_KEY"] != "token" || args.Start.NamedSSHKeys == nil || args.Start.NamedSSHKeys.Values["DEPLOY_KEY"] != "synthetic-key" {
				t.Fatal("commit lost its actual fresh member or named-key producers")
			}
			result := api.RewindResult{ID: selection.ID, Checkpoint: selection.Checkpoint, Session: Session{ID: selection.ID, SandboxOf: parent.SessionID, Wall: &api.WallInfo{Level: "microvm"}, RunGeneration: strings.Repeat("e", 32), SecretState: rewindFixturePublicStateV3("d"), Secrets: []string{"API_KEY"}, NamedSSHKeys: &api.NamedSSHKeyViewV1{V: 1, Installed: []string{"DEPLOY_KEY"}}}}
			for _, change := range mutate {
				change(&result)
			}
			return result
		case api.OpRewindPreparedCancelV1:
			return struct{}{}
		default:
			t.Fatalf("rewind borrowed an ordinary resume operation: %s", request.Op)
			return nil
		}
	})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, r, ctx, selection, marker, commits
}

func TestOriginalRewindProducerFetchesAllMembersAndNamedKeysOnce(t *testing.T) {
	c, _, ctx, selection, marker, commits := rewindClientFixtureV1(t, false)
	prepared, err := c.PrepareRewind(ctx, selection, RewindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PrepareRewind(ctx, selection, RewindOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchSessionSecrets(ctx, c.Host, selection.ID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("a generic fetch escaped the original producer's cancellation/join lifetime")
	}
	if mode, err := retainedNativeResumeModeV1(ctx, c, selection.ID); err != nil || mode != "rewind" {
		t.Fatalf("rewind impersonated native cold absence: %s %v", mode, err)
	}
	if result, err := prepared.Commit(ctx); err != nil || result.ID != selection.ID || *commits != 1 {
		t.Fatalf("original rewind commit = %v %v", result, err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "AAK" {
		t.Fatalf("values were skipped or fetched again: %q %v", data, err)
	}
	if _, err := prepared.Commit(ctx); err != nil || *commits != 1 {
		t.Fatal("an acknowledged retry replayed restore or lost its original receipt", err)
	}
	for id := range prepared.group.members {
		row, err := LoadSandboxPolicy(ctx, c.Host, id)
		if err != nil || row == nil || row.deliveryReceipt == nil || row.deliveryReceipt.Version != 3 || row.deliveryReceipt.Operation != "rewind" || row.deliveryReceipt.Scope.RunGeneration != strings.Repeat("e", 32) || row.deliveryReceipt.Scope.BackendGeneration != strings.Repeat("d", 32) {
			t.Fatal("fresh rewind publication did not update every original local member", err)
		}
	}
}

func rewindFixturePublicStateV3(generation string) *api.SessionSecretState {
	return &api.SessionSecretState{Version: 1, Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"API_KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}, Generation: strings.Repeat(generation, 32), Primary: map[string]api.PublicSecretState{"API_KEY": {Mode: api.SecretModeExposed, Armed: true}}}
}

func TestRewindChangedRemoteSourceStopsBeforeFirstValueCommand(t *testing.T) {
	c, _, ctx, selection, marker, commits := rewindClientFixtureV1(t, true)
	if _, err := c.PrepareRewind(ctx, selection, RewindOptions{}); err == nil {
		t.Fatal("changed original source was accepted")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) || *commits != 0 {
		t.Fatal("a changed source reached value commands or restore")
	}
}

func TestRewindRetainsOriginalSettingsAndRefusesGenericCommitDTO(t *testing.T) {
	c, _, ctx, selection, _, commits := rewindClientFixtureV1(t, false)
	prepared, err := c.PrepareRewind(ctx, selection, RewindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	args := api.RewindCommitArgsV1{V: 1, Prepared: &api.PreparedRewindV1{V: 1, Token: strings.Repeat("c", 64)}, Selection: selection, Start: ResumeArgs{ID: selection.ID, Snapshot: prepared.group.snapshot.Clone()}}
	if err := c.Call(ctx, api.OpRewindCommitV1, args, nil); err == nil || *commits != 0 {
		t.Fatal("a guessed token DTO acquired the original private producer")
	}
	writeRetainedImagePolicyFixture(t, "# replaced after preparation\n")
	if _, err := prepared.Commit(ctx); err == nil || *commits != 0 {
		t.Fatal("changed original settings reached the physical commit")
	}
}
