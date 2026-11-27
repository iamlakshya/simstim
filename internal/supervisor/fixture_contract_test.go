package supervisor

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// A scripted guest still needs an explicitly owned fixture volume. Leaving
// that private factory capability out must fail before its Start is called.
func TestMissingFactoryDiskAuthorityRefusesBeforeSandboxStart(t *testing.T) {
	dir := testFolders(t)
	const id = "nocap234"
	now := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "box", CreatedAt: now, Cwd: dir,
		Agent:  host.AgentInfo{Kind: "custom", Argv: []string{"/bin/sh"}},
		Status: host.Status{State: host.StateStarting, Since: now},
		Wall:   &host.WallRecord{Level: "none", Driver: "fake", Version: version.Version, At: now}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	var started atomic.Bool
	old, oldFake := sandboxFor, testFakeSandbox
	sandboxFor, testFakeSandbox = func(*host.Manifest) (sandbox, error) { return &startSpy{started: &started}, nil }, true
	t.Cleanup(func() { sandboxFor, testFakeSandbox = old, oldFake })
	var failure *proto.Error
	err := Run(id)
	if !errors.As(err, &failure) || failure.Code != "disk_measure" || failure.Hint == "" || started.Load() {
		t.Fatal("unissued disk authority reached Start or lost its refusal", err)
	}
	saved, err := host.LoadManifest(id)
	if err != nil || saved.Life == nil || saved.Life.End == nil || saved.Life.End.Kind != host.EndBoot || !saved.Life.Started.IsZero() {
		t.Fatal("unissued disk authority published a successful boot", saved, err)
	}
}

// Successful fixture replies carry the claimed ABI required by their driver.
// Missing or other-driver metadata must still refuse the actual boot path.
func TestWrongGuestABIRefusesBeforeStartedPublication(t *testing.T) {
	for _, abi := range []string{"", proto.GuestPlainTestV1, proto.GuestOwnedPTYV2} {
		t.Run(abi, func(t *testing.T) {
			h := hostileStart(t)
			request := h.readReq(t)
			if request.Op != proto.CtlStart {
				t.Fatal("unexpected initial operation", request.Op)
			}
			raw, err := json.Marshal(proto.CtlStartResult{AgentPID: 7, GuestABI: abi})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.w.WriteJSON(proto.TResponse, proto.Response{ID: request.ID, OK: true, Result: raw}); err != nil {
				t.Fatal(err)
			}
			var failure *proto.Error
			if err := h.waitEnd(t, 10*time.Second); !errors.As(err, &failure) || failure.Code != "exec_failed" {
				t.Fatal("wrong-driver guest ABI was accepted", err)
			}
			saved, err := host.LoadManifest(h.id)
			if err != nil || saved.Life == nil || saved.Life.End == nil || saved.Life.End.Kind != host.EndBoot || !saved.Life.Started.IsZero() {
				t.Fatal("wrong-driver guest ABI published Started", saved, err)
			}
		})
	}
}
