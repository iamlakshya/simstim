package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func lockdownConnectionFixtureV1(t *testing.T, on bool) (*Conn, *runtimeDeliveryPeer, Host) {
	t.Helper()
	conn, peer, host := heldRequestConnectionFixture(t, func(req proto.Request) (any, *proto.Error) {
		var args api.PolicyLockdownArgsV1
		if req.Op != "policy.lockdown" || json.Unmarshal(req.Args, &args) != nil || args.Version != 1 || args.On != on || args.Session != "" || args.PreviousHash != "" {
			t.Error("lockdown sent something beyond its fixed host restriction", req.Op)
		}
		return api.HostLockdownResultV1{Version: 1, On: on, Sessions: []api.PolicyLockdownResultV1{{Version: 1, On: on, Session: "root2345", State: "applied", Hash: strings.Repeat("b", 64)}}, Unreached: []string{}}, nil
	})
	conn.Features = append(conn.Features, api.FeaturePolicyLockdownV1)
	return conn, peer, host
}

func TestLockdownOffNeedsLiteralBeforeAnyLocalOrHostMutation(t *testing.T) {
	isolateH5GrantHome(t)
	for _, literal := range []string{"", "yes", "LOCKDOWN"} {
		if _, err := NewPolicyLockdownRequestV1(Host{Name: "box"}, false, literal); err == nil {
			t.Fatal("another word authorised off", literal)
		}
	}
	if _, err := os.Lstat(StateDir()); !os.IsNotExist(err) {
		t.Fatal("refused off created local state", err)
	}
}

func TestLockdownOffFromAnotherAcceptedLaptopNeedsNoCreationReceiptOrConfig(t *testing.T) {
	conn, peer, host := lockdownConnectionFixtureV1(t, false)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte("[broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sandboxPath := filepath.Join(StateDir(), "hosts", HostFileKey(host.Name), "sandboxes.json")
	before, beforeErr := os.ReadFile(sandboxPath)
	request, err := NewPolicyLockdownRequestV1(host, false, "lockdown")
	if err != nil {
		t.Fatal(err)
	}
	defer request.Close()
	ctx := request.Context(t.Context())
	change, err := BeginPolicyChange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer change.Close()
	result, err := request.Apply(change.Context(ctx), conn)
	if err != nil || result.On || len(result.Sessions) != 1 || peer.count("policy.lockdown", "") != 1 {
		t.Fatal("off tried to select local layers, receipts or values", result, err)
	}
	after, afterErr := os.ReadFile(sandboxPath)
	if !bytes.Equal(before, after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) {
		t.Fatal("off fabricated or changed a local creation record", afterErr)
	}
}

func TestLockdownRejectsGenericCallAndReaddedHostSource(t *testing.T) {
	conn, peer, host := lockdownConnectionFixtureV1(t, true)
	if err := conn.Call(t.Context(), "policy.lockdown", api.PolicyLockdownArgsV1{Version: 1, On: true}, nil); err == nil || peer.count("policy.lockdown", "") != 0 {
		t.Fatal("generic call bypassed the fixed local intent", err)
	}
	request, err := NewPolicyLockdownRequestV1(host, true, "")
	if err != nil {
		t.Fatal(err)
	}
	defer request.Close()
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{host}); err != nil {
		t.Fatal(err)
	}
	ctx := request.Context(t.Context())
	change, err := BeginPolicyChange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer change.Close()
	if _, err := request.Apply(change.Context(ctx), conn); err == nil || peer.count("policy.lockdown", "") != 0 {
		t.Fatal("a readded name renewed the original accepted Source", err)
	}
}

func TestLockdownPendingRejectsMissingOrNullOffIntent(t *testing.T) {
	binding := `{"entry_hash":"` + strings.Repeat("a", 64) + `"}`
	for _, intent := range []string{`{"binding":` + binding + `}`, `{"on":null,"binding":` + binding + `}`, `{"On":false,"binding":` + binding + `}`, `{"on":false,"binding":{"entry_hash":"` + strings.Repeat("a", 64) + `","host_id":null}}`} {
		raw := []byte(`{"box":` + intent + `}`)
		if err := validateLockdownPendingRawV1(raw); err == nil {
			t.Fatal("ambiguous state became an off action", string(raw))
		}
	}
}

func TestLockdownReplayOffWinsInterruptedLocalOnCleanup(t *testing.T) {
	conn, peer, host := lockdownConnectionFixtureV1(t, false)
	binding, err := lockdownBindingForHostV1(host)
	if err != nil {
		t.Fatal(err)
	}
	change, err := BeginPolicyChange(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx := change.Context(t.Context())
	if err := updateLocalLockdownV1(ctx, map[string]policyLockdownBindingV1{host.Name: binding}, nil); err != nil {
		t.Fatal(err)
	}
	if err := updateLockdownPendingV1(ctx, map[string]policyLockdownPendingV1{host.Name: {On: false, Binding: binding}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := change.Close(); err != nil {
		t.Fatal(err)
	}
	// The command stopped after recording off and before clearing local on.
	if err := ReplayPendingPolicy(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if peer.count("policy.lockdown", "") != 1 {
		t.Fatal("recorded off was not sent exactly once")
	}
	if _, err := os.Lstat(filepath.Join(StateDir(), policyLockdownFileV1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("off replay retained an on mark", err)
	}
	state, err := readPolicyPending(t.Context())
	if err != nil || len(state.Lockdowns) != 0 {
		t.Fatal("acknowledged fixed action remained pending", err)
	}
	if err := ReplayPendingPolicy(t.Context(), conn); err != nil || peer.count("policy.lockdown", "") != 1 {
		t.Fatal("unrecorded contact repeated a completed restriction change", err)
	}
}

func TestLockdownOffLocalFileRemovalPreservesFutureMetadata(t *testing.T) {
	isolateH5GrantHome(t)
	change, err := BeginPolicyChange(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer change.Close()
	path := filepath.Join(StateDir(), policyLockdownFileV1)
	raw := []byte(`{"v":2,"hosts":{},"future":{"retain":true}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := updateLocalLockdownV1(change.Context(t.Context()), nil, []string{"box"}); err == nil {
		t.Fatal("off discarded future local metadata")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatal("failed off changed future bytes", err)
	}
}
