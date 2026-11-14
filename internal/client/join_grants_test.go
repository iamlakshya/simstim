package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func sharedGrantFixture(t *testing.T) (Host, SandboxPolicy, *Conn, *GrantRequest) {
	t.Helper()
	h := h5GrantState(t)
	p := h5Policy()
	p.Ceiling, p.CreationWall, p.Exposed = TierOwn, "process", []string{"API_KEY"}
	p.PolicySources = json.RawMessage(`{ "future": {"duplicate":1,"duplicate":2} }`)
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	saved, err := LoadSandboxPolicy(t.Context(), h, p.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	p = *saved
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nAPI_KEY = \"printf fixture-value\"\nOTHER_KEY = \"printf forbidden-value\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Conn{Host: h, hostID: p.HostID, Features: []string{"resume", api.FeatureSpawn, "sandbox-snapshot-v1", "secrets"},
		pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	return h, p, c, NewGrantRequest(false, false)
}

func sharedGrantSnapshot(primary string, members ...string) *api.SandboxSnapshot {
	return &api.SandboxSnapshot{Version: 1, Primary: primary, Generation: strings.Repeat("a", 64), Members: append([]string{}, members...)}
}

func sharedGrantWire(t *testing.T, c *Conn, handle func(proto.Request) any) {
	t.Helper()
	c.w = proto.NewWriter(grantWriteFunc(func(data []byte) (int, error) {
		frame, err := proto.Read(bufio.NewReader(bytes.NewReader(data)))
		if err != nil {
			return 0, err
		}
		var req proto.Request
		if err := json.Unmarshal(frame.Payload, &req); err != nil {
			return 0, err
		}
		result, err := json.Marshal(handle(req))
		if err != nil {
			return 0, err
		}
		c.mu.Lock()
		ch := c.pending[req.ID]
		delete(c.pending, req.ID)
		c.mu.Unlock()
		ch <- proto.Response{ID: req.ID, OK: true, Result: result}
		return len(data), nil
	}))
}

func TestJoinUsesOnlyLocalNamesAndPersistsBeforeOneFrame(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	// A current laptop expansion must not enlarge this parent's allowance.
	c.Host.Secrets = []string{"API_KEY", "OTHER_KEY"}
	h.Secrets = slices.Clone(c.Host.Secrets)
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	var queries, spawns int
	sharedGrantWire(t, c, func(req proto.Request) any {
		switch req.Op {
		case "sandbox.snapshot":
			queries++
			return sharedGrantSnapshot(parent.SessionID)
		case "spawn":
			spawns++
			var a api.JoinArgs
			if err := json.Unmarshal(req.Args, &a); err != nil {
				t.Fatal(err)
			}
			child, err := LoadSandboxPolicy(t.Context(), h, a.ID)
			if err != nil || child == nil || child.SSHAgent || child.lineage == nil ||
				child.lineage.Parent != parent.SessionID || child.lineage.Root != parent.SessionID ||
				!slices.Equal(child.lineage.Allowed, []string{"API_KEY"}) ||
				!bytes.Equal(child.PolicySources, parent.PolicySources) || !bytes.Equal(child.wallIntent, parent.wallIntent) {
				t.Fatalf("child publication preceded exact inherited facts: %+v, %v", child, err)
			}
			if a.Parent != parent.SessionID || a.Snapshot == nil || string(a.Secrets["API_KEY"]) != "fixture-value" {
				t.Fatal("spawn changed the original parent, snapshot or ephemeral values")
			}
			if _, release, err := LockHost(t.Context(), h.Name, "competing mutation"); err == nil {
				release()
				t.Fatal("the complete frame was not protected")
			}
			return Session{ID: a.ID}
		default:
			t.Fatalf("unexpected RPC %q", req.Op)
			return nil
		}
	})
	ctx := WithGrantRequest(t.Context(), request)
	names, err := c.JoinSecretNames(ctx, parent.SessionID)
	if err != nil || !slices.Equal(names, []string{"API_KEY"}) {
		t.Fatalf("join names=%v, %v", names, err)
	}
	if err := c.CheckJoinGrants(ctx, parent.SessionID, names); err != nil {
		t.Fatal(err)
	}
	child, err := c.JoinSession(ctx, api.JoinArgs{Parent: parent.SessionID, Secrets: map[string]api.Secret{"API_KEY": "fixture-value"}})
	if err != nil || child == nil || queries != 1 || spawns != 1 {
		t.Fatalf("join queries=%d spawns=%d child=%v err=%v", queries, spawns, child, err)
	}
	raw, err := os.ReadFile(filepath.Join(StateDir(), "hosts", HostFileKey(h.Name), "sandboxes.json"))
	if err != nil || bytes.Contains(raw, []byte("fixture-value")) || !bytes.Contains(raw, []byte(`"schema":3`)) {
		t.Fatalf("local child state leaked values or missed the old-reader barrier: %v", err)
	}
}

func TestJoinUnknownParentRejectsEvenScopedNamesAndCreatesNoRecord(t *testing.T) {
	h, _, c, request := sharedGrantFixture(t)
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nAPI_KEY = \"printf forbidden\"\n[secret_tiers]\nAPI_KEY = \"throwaway\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const unknown = "uvw234xy"
	var spawns int
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == "sandbox.snapshot" {
			return sharedGrantSnapshot(unknown)
		}
		spawns++
		var a api.JoinArgs
		_ = json.Unmarshal(req.Args, &a)
		return Session{ID: a.ID}
	})
	ctx := WithGrantRequest(t.Context(), request)
	if err := c.CheckJoinGrants(ctx, unknown, []string{"API_KEY"}); err == nil || spawns != 0 {
		t.Fatal("unknown authority accepted values")
	}
	names, err := c.JoinSecretNames(ctx, unknown)
	if err != nil || len(names) != 0 {
		t.Fatalf("unknown no-value names=%v err=%v", names, err)
	}
	child, err := c.JoinSession(ctx, api.JoinArgs{Parent: unknown, Agent: "shell"})
	if err != nil || spawns != 1 {
		t.Fatalf("genuine no-value join refused: %v", err)
	}
	if p, err := LoadSandboxPolicy(t.Context(), h, child.ID); err != nil || p != nil {
		t.Fatalf("unknown authority was fabricated: %+v, %v", p, err)
	}
}

func TestJoinNoValueStillKeepsGlobalInvalidSettingsBarrier(t *testing.T) {
	_, p, c, request := sharedGrantFixture(t)
	if err := os.WriteFile(settingsPath(), []byte("[secret_tiers]\nUNDEFINED_KEY = \"invalid\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op != "sandbox.snapshot" {
			t.Fatal("invalid settings published a spawn")
		}
		return sharedGrantSnapshot(p.SessionID)
	})
	if _, err := c.JoinSession(WithGrantRequest(t.Context(), request), api.JoinArgs{Parent: p.SessionID, Agent: "shell"}); err == nil {
		t.Fatal("no-value join skipped global settings validation")
	}
}

func TestJoinFeatureAndLocalRootRefuseBeforeAnyValue(t *testing.T) {
	_, p, c, r := sharedGrantFixture(t)
	ctx := WithGrantRequest(t.Context(), r)
	c.Features = []string{api.FeatureSpawn, "secrets"}
	if err := c.CheckJoinGrants(ctx, p.SessionID, []string{"API_KEY"}); err == nil {
		t.Fatal("provisional spawn without snapshot capability was accepted")
	}
	c.Features = append(c.Features, "sandbox-snapshot-v1")
	sharedGrantWire(t, c, func(proto.Request) any { return sharedGrantSnapshot("uvw234xy", p.SessionID) })
	if _, err := c.JoinSecretNames(ctx, p.SessionID); err == nil {
		t.Fatal("a host snapshot retargeted known local root authority")
	}
}

func TestJoinRetryCannotGrowOrRetargetAndCancellationWritesNothing(t *testing.T) {
	_, p, c, r := sharedGrantFixture(t)
	sharedGrantWire(t, c, func(proto.Request) any { return sharedGrantSnapshot(p.SessionID) })
	ctx := WithGrantRequest(t.Context(), r)
	if err := c.CheckJoinGrants(ctx, p.SessionID, []string{"API_KEY"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckJoinGrants(ctx, p.SessionID, nil); err == nil {
		t.Fatal("one request changed selected names")
	}
	if err := c.CheckJoinGrants(ctx, "uvw234xy", nil); err == nil {
		t.Fatal("one request changed original parent")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	c.w = proto.NewWriter(grantWriteFunc(func([]byte) (int, error) { t.Fatal("cancelled join wrote a frame"); return 0, nil }))
	if _, err := c.JoinSession(cancelled, api.JoinArgs{Parent: p.SessionID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled join=%v", err)
	}
}

func TestJoinRechecksLocalParentBeforeFetchAndPublication(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	sharedGrantWire(t, c, func(proto.Request) any { return sharedGrantSnapshot(parent.SessionID) })
	ctx := WithGrantRequest(t.Context(), request)
	if err := c.CheckJoinGrants(ctx, parent.SessionID, []string{"API_KEY"}); err != nil {
		t.Fatal(err)
	}
	parent.Ceiling = TierThrowaway
	if err := reserveSandbox(t.Context(), h, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("a lowered local parent ceiling did not stop the first read")
	}
	c.w = proto.NewWriter(grantWriteFunc(func([]byte) (int, error) { t.Fatal("lowered parent published"); return 0, nil }))
	if _, err := c.JoinSession(ctx, api.JoinArgs{Parent: parent.SessionID, Secrets: map[string]api.Secret{"API_KEY": "must-not-send"}}); err == nil {
		t.Fatal("a final Join ignored the parent's current ceiling")
	}
}

func TestJoinFinalGateObservesTierChangedDuringSnapshotRead(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op != "sandbox.snapshot" {
			t.Fatal("the final grant gate used the pre-snapshot tier")
		}
		if err := SetTierContext(t.Context(), h.Name, TierThrowaway, "", ""); err != nil {
			t.Fatal(err)
		}
		return sharedGrantSnapshot(parent.SessionID)
	})
	_, err := c.JoinSession(WithGrantRequest(t.Context(), request), api.JoinArgs{Parent: parent.SessionID, Secrets: map[string]api.Secret{"API_KEY": "must-not-publish"}})
	if err == nil {
		t.Fatal("a pre-lock tier snapshot authorised the final frame")
	}
	if rows, err := GrantsForHostContext(t.Context(), h, parent.HostID); err != nil || len(rows) != 0 {
		t.Fatalf("a failed final gate recorded an exposure: %+v %v", rows, err)
	}
}

func TestJoinRetryKeepsRequestAcrossContextAndConnection(t *testing.T) {
	_, parent, first, request := sharedGrantFixture(t)
	sharedGrantWire(t, first, func(proto.Request) any { return sharedGrantSnapshot(parent.SessionID) })
	if err := first.CheckJoinGrants(WithGrantRequest(t.Context(), request), parent.SessionID, []string{"API_KEY"}); err != nil {
		t.Fatal(err)
	}
	next := &Conn{Host: cloneGrantHost(first.Host), hostID: first.HostID(), Features: slices.Clone(first.Features),
		pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	sharedGrantWire(t, next, func(req proto.Request) any {
		if req.Op != "spawn" {
			t.Fatal("a reconnect forgot the pinned structural snapshot")
		}
		var a api.JoinArgs
		_ = json.Unmarshal(req.Args, &a)
		return Session{ID: a.ID}
	})
	ctx, cancel := context.WithCancel(WithGrantRequest(t.Context(), request))
	defer cancel()
	if _, err := next.JoinSession(ctx, api.JoinArgs{Parent: parent.SessionID, Secrets: map[string]api.Secret{"API_KEY": "fixture-value"}}); err != nil {
		t.Fatalf("same logical request could not retry with its original host: %v", err)
	}
}
