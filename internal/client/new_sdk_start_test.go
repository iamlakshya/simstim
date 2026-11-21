package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

func sdkStartSourceFixtureV1(t *testing.T) (*Conn, *GrantRequest, *int) {
	t.Helper()
	h := h5GrantState(t)
	h.WallFloor = "process"
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "sandbox", "wall.process", PolicyLayerFeature, inputtransfer.Feature, inputtransfer.FinishFeature)
	c.pending = map[uint64]chan proto.Response{}
	probes := new(int)
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op != "sandbox.probe" {
			t.Fatal("SDK preparation issued an unrelated RPC", req.Op)
		}
		*probes++
		return api.CapProbe{Walls: []api.WallProbe{{Level: "process", OK: true}, {Level: "microvm", OK: true}}}
	})
	r := NewGrantRequest(false, false)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, r, probes
}

func TestSDKNewStartPreparesPinnedWallAndReusesActualPrivatePlans(t *testing.T) {
	c, r, probes := sdkStartSourceFixtureV1(t)
	ctx := WithGrantRequest(t.Context(), r)
	raw := NewArgs{Name: "sdk-start", Argv: []string{"/bin/sh", "-c", "printf prepared"}, Cols: 80, Rows: 24}
	a, err := prepareSDKNewStartV1(ctx, c, raw)
	if err != nil {
		t.Fatal(err)
	}
	if a.Wall != "process" || a.Floor != "process" || a.DowngradedFrom != "" || !slices.Equal(a.Argv, raw.Argv) ||
		a.Inputs == nil || a.Environment == nil || r.sdkStart == nil || r.newInputs == nil || r.newSecrets == nil ||
		r.newInputs.operation != r.sdkStart.operation || r.newSecrets.operation != r.sdkStart.operation || !r.newSecrets.fetched || *probes != 1 {
		t.Fatal("SDK start did not use the complete original prepared path")
	}
	inputs, secrets, operation := r.newInputs, r.newSecrets, r.sdkStart.operation
	again, err := prepareSDKNewStartV1(ctx, c, raw)
	if err != nil || r.newInputs != inputs || r.newSecrets != secrets || r.sdkStart.operation != operation ||
		again.ID != a.ID || !again.Inputs.Equal(*a.Inputs) || r.sdkStart.original.Wall != "" || len(r.sdkStart.original.Secrets) != 0 {
		t.Fatal("SDK retry replaced a private capture or its original raw intent", err)
	}
}

func TestSDKNewStartCannotRetargetOriginalCommandOrConnection(t *testing.T) {
	c, r, _ := sdkStartSourceFixtureV1(t)
	ctx := WithGrantRequest(t.Context(), r)
	raw := NewArgs{Argv: []string{"/bin/sh", "-c", "printf original"}}
	if _, err := prepareSDKNewStartV1(ctx, c, raw); err != nil {
		t.Fatal(err)
	}
	other := finishGeneratedTransportFixture(t, c.Host, c.HostID(), c.Features...)
	if _, err := prepareSDKNewStartV1(ctx, other, raw); err == nil {
		t.Fatal("another connection adopted the original SDK operation")
	}
	raw.Argv[2] = "printf replacement"
	if _, err := prepareSDKNewStartV1(ctx, c, raw); err == nil || r.sdkStart.original.Argv[2] != "printf original" {
		t.Fatal("mutable caller arguments changed the original command")
	}
}

func TestSDKNewStartKeepsOrdinaryFolderAndSetupQuestionAnswers(t *testing.T) {
	c, r, _ := sdkStartSourceFixtureV1(t)
	ctx := WithGrantRequest(t.Context(), r)
	raw := NewArgs{Name: "sdk-question", Agent: "shell"}
	first, err := prepareSDKNewStartV1(ctx, c, raw)
	if err != nil {
		t.Fatal(err)
	}
	raw.MakeFolder = true
	raw = AnswerSetup(raw, SetupQuestion{Repo: "~/work", Hash: strings.Repeat("c", 64)}, true)
	again, err := prepareSDKNewStartV1(ctx, c, raw)
	if err != nil || !again.MakeFolder || again.SetupHashes["~/work"] != strings.Repeat("c", 64) || again.ID != first.ID ||
		r.sdkStart.original.MakeFolder || len(r.sdkStart.original.SetupHashes) != 0 {
		t.Fatal("ordinary answered questions changed the original identity or were discarded", err)
	}
}

func TestSDKNewStartPreservesExplicitMemoryAutoAcrossPreparedRetry(t *testing.T) {
	for _, explicitAuto := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured-default", true: "explicit-auto"}[explicitAuto], func(t *testing.T) {
			c, r, _ := sdkStartSourceFixtureV1(t)
			if err := os.WriteFile(settingsPath(), []byte("[sessions]\nmemory_max='4G'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			raw := NewArgs{Agent: "shell"}
			expected := int64(4 << 30)
			if explicitAuto {
				raw.MemoryMax, expected = MemoryAuto, 0
			}
			ctx := WithGrantRequest(t.Context(), r)
			first, err := prepareSDKNewStartV1(ctx, c, raw)
			if err != nil || first.MemoryMax != expected {
				t.Fatal("first SDK preparation lost its real auto or configured choice", first.MemoryMax, err)
			}
			again, err := prepareSDKNewStartV1(ctx, c, raw)
			if err != nil || again.MemoryMax != expected || again.ID != first.ID {
				t.Fatal("SDK retry changed the original memory choice", again.MemoryMax, err)
			}
		})
	}
}

func TestSDKNewStartCannotRenewRemovedAndReaddedSource(t *testing.T) {
	c, r, probes := sdkStartSourceFixtureV1(t)
	ctx := WithGrantRequest(t.Context(), r)
	raw := NewArgs{Agent: "shell"}
	if _, err := prepareSDKNewStartV1(ctx, c, raw); err != nil {
		t.Fatal(err)
	}
	original := r.sdkStart
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{c.Host}); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSDKNewStartV1(ctx, c, raw); err == nil || r.sdkStart != original || *probes != 1 {
		t.Fatal("an old SDK plan adopted a new saved host membership", err)
	}
}

func TestSDKNewStartCannotExtendOriginalCancelledLifetime(t *testing.T) {
	c, r, _ := sdkStartSourceFixtureV1(t)
	base, cancel := context.WithCancel(t.Context())
	ctx := WithGrantRequest(base, r)
	raw := NewArgs{Agent: "shell"}
	if _, err := prepareSDKNewStartV1(ctx, c, raw); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := prepareSDKNewStartV1(WithGrantRequest(t.Context(), r), c, raw); err == nil {
		t.Fatal("fresh retry context extended the original preparation lifetime")
	}
}

func TestControlStartPublishesRecordedNoneWithoutConfiguredInputsOrValues(t *testing.T) {
	c, r, _ := sdkStartSourceFixtureV1(t)
	marker := filepath.Join(t.TempDir(), "must-not-fetch")
	config := "[secrets]\nCONTROL_KEY=" + strconv.Quote(secretSettingsMarkerCommand(marker)) + "\n[sandbox]\nsecrets=['CONTROL_KEY']\nenv={MODE='configured'}\n"
	if err := os.WriteFile(settingsPath(), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), r)
	raw := NewArgs{Name: "login-example", Wall: "none", Downgrade: true, Cwd: "~", Argv: []string{"/bin/sh", "-c", "printf login"}, Setup: api.SetupSkip}
	a, err := PrepareNewSessionArgs(ctx, raw, 80, 24)
	if err == nil {
		a, err = c.PrepareUnwalledControlStartV1(ctx, a)
	}
	if err != nil {
		t.Fatal(err)
	}
	var outgoing NewArgs
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op != "new" {
			t.Fatal("control start selected sandbox inputs or runtime", req.Op)
		}
		if err := json.Unmarshal(req.Args, &outgoing); err != nil {
			t.Fatal(err)
		}
		return Session{ID: outgoing.ID}
	})
	s, err := c.NewSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadSandboxPolicy(ctx, c.Host, s.ID)
	if err != nil || p == nil || p.CreationWall != "none" || outgoing.Wall != "none" || outgoing.Floor != "process" || outgoing.DowngradedFrom != "process" ||
		outgoing.Name != raw.Name || !slices.Equal(outgoing.Argv, raw.Argv) || outgoing.Setup != raw.Setup || len(outgoing.Secrets) != 0 ||
		outgoing.Inputs != nil || outgoing.Environment != nil || r.newInputs != nil || r.newSecrets != nil || r.sdkStart != nil {
		t.Fatal("control start lost its recorded downgrade or selected configured payload", err)
	}
	requireSecretSettingsMarkerAbsent(t, marker)
}

func TestControlStartCannotAcceptPreparedAuthorityOrImplicitNone(t *testing.T) {
	c, r, probes := sdkStartSourceFixtureV1(t)
	ctx := WithGrantRequest(t.Context(), r)
	for _, a := range []NewArgs{
		{}, {Wall: "none"}, {Wall: "auto", Downgrade: true},
		{Wall: "none", Downgrade: true, Inputs: &api.InputSummary{}},
		{Wall: "none", Downgrade: true, Environment: &api.SessionEnvironment{}},
		{Wall: "none", Downgrade: true, Layers: []api.Layer{{Kind: api.LayerSession}}},
		{Wall: "none", Downgrade: true, Secrets: map[string]api.Secret{"CONTROL_KEY": "caller-value"}},
		{Wall: "none", Downgrade: true, NamedSSHKeys: &api.NamedSSHKeysV1{}},
		{Wall: "none", Downgrade: true, ProtectedTrustedSecretValues: map[string]api.Secret{"CONTROL_KEY": "caller-value"}},
		{Wall: "none", Downgrade: true, SSHAgent: true},
	} {
		if _, err := c.PrepareUnwalledControlStartV1(ctx, a); err == nil || r.wall != nil || r.preparationSettings != nil || *probes != 0 {
			t.Fatal("an implicit or DTO control start acquired authority", err)
		}
	}
}
