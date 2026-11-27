package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestTrustedStoreAllocationUsesSharedActualTrustAndFiltersOnlyMetadata(t *testing.T) {
	primary := proto.CtlStartArgs{Session: "primary234", RunToken: "run/primary234", Agent: "codex", Argv: []string{"codex"}, Cwd: "/work", Env: []string{"HOME=/home/agent", "HTTP_PROXY=" + host.DefaultGuestProxy, "CODEX_HOME=/home/agent/.codex", brand.Env("SESSION") + "=primary234", "TERM=xterm"}, Secrets: map[string]string{"OPENAI_API_KEY": "public-sentinel"}, SecretModes: map[string]api.SecretMode{"OPENAI_API_KEY": api.SecretModeBound}}
	child := primary
	child.Session = "child234"
	child.RunToken = "run/child234"
	child.Env = append([]string(nil), primary.Env...)
	child.Env[len(child.Env)-2] = brand.Env("SESSION") + "=child234"
	child.Env[len(child.Env)-1] = "TERM=screen"
	child.CACert = ""
	child.Proxy = ""
	s := &Supervisor{id: primary.Session}
	if err := s.planMemberStores([]proto.CtlStartArgs{primary, child}, true, host.DefaultGuestProxy); err != nil {
		t.Fatal(err)
	}
	if s.storeBatch == nil || !s.memberStores[primary.Session] || !s.memberStores[child.Session] {
		t.Fatal("required member sub-ops lost unique shared allocation")
	}
	environment := strings.Join(s.storeBatch.env, "\n")
	if !strings.Contains(environment, "CODEX_CA_CERTIFICATE=") || !strings.Contains(environment, "CODEX_HOME=") || !strings.Contains(environment, "HTTP_PROXY=") || strings.Contains(environment, "public-sentinel") || strings.Contains(environment, brand.Env("SESSION")+"=") || strings.Contains(environment, "TERM=") {
		t.Fatal("trusted planning lost actual trust/proxy/state projection or added material")
	}
	original := s.storeBatch
	original.allocated = true
	if err := s.planMemberStores([]proto.CtlStartArgs{child}, true, host.DefaultGuestProxy); err != nil {
		t.Fatal(err)
	}
	if s.storeBatch != original || !s.storeBatch.allocated {
		t.Fatal("later identical member received a new Store allocation")
	}
	child.Cwd = "/other"
	if err := s.planMemberStores([]proto.CtlStartArgs{child}, true, host.DefaultGuestProxy); err == nil {
		t.Fatal("changed Store context received another target")
	}
}
func TestTrustedStoreAllocationNeverInfersExposedOrUnknownAgentStore(t *testing.T) {
	a := proto.CtlStartArgs{Session: "primary234", RunToken: "run/primary234", Agent: "codex", Argv: []string{"codex"}, Cwd: "/work", Env: []string{"HOME=/home/agent"}, Secrets: map[string]string{"OPENAI_API_KEY": "supplied-exposed"}, SecretModes: map[string]api.SecretMode{"OPENAI_API_KEY": api.SecretModeExposed}}
	for _, kind := range []string{"codex", "custom-command"} {
		a.Agent = kind
		s := &Supervisor{id: a.Session}
		if err := s.planMemberStores([]proto.CtlStartArgs{a}, true, host.DefaultGuestProxy); err != nil {
			t.Fatal(err)
		}
		if s.storeBatch != nil || s.memberStores[a.Session] {
			t.Fatal("exposed/unknown agent invented Store work")
		}
	}
}

func TestUnconfirmedStoreBlocksLaterZeroStoreAndSpawnBeforeAnyControl(t *testing.T) {
	for _, args := range []proto.CtlStartArgs{{Session: "child234", RunToken: "run/child234"}, {Session: "child234", RunToken: "run/child234", SecretModes: map[string]api.SecretMode{}}} {
		s := &Supervisor{id: "parent234", m: &host.Manifest{ID: "parent234", Wall: &host.WallRecord{Driver: "process"}}, rec: record{phase: host.LifeRunning}, storeBatch: &trustedStoreBatch{allocated: true}}
		if err := s.prepareMemberPhases(nil, &args, proto.CtlSpawn, true); err == nil {
			t.Fatal("a later member bypassed the unconfirmed Store")
		}
		var failure *proto.Error
		_, err := s.spawn(api.JoinArgs{})
		if !errors.As(err, &failure) || failure.Code != "secret_store" || !s.memberFault || args.PreparedClaim != "" || len(s.members) != 0 {
			t.Fatal("later zero-Store/Spawn reached policy or control after lost ACK", err)
		}
	}
}
func TestAcknowledgedDuplicateStoreUsesOnlyRemainingActiveBudget(t *testing.T) {
	oldClock := phaseclock.Current
	clock := &logicalClock{current: time.Now()}
	phaseclock.Current = clock
	t.Cleanup(func() { phaseclock.Current = oldClock })
	parent, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	local, remote := net.Pipe()
	owner := link.New(local, link.Supervisor, link.Options{})
	guest := link.New(remote, link.Guest, link.Options{})
	accepted := make(chan *link.Stream, 1)
	go func() { stream, _ := guest.Accept(parent); accepted <- stream }()
	stream, err := owner.Open(parent, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	if peer == nil {
		t.Fatal("control peer unavailable")
	}
	s := &Supervisor{id: "parent234", m: &host.Manifest{ID: "parent234"}, lifetime: parent, link: owner, rec: record{phase: host.LifeRunning}}
	s.ctl = newCtl(s, stream)
	t.Cleanup(func() { _ = owner.Close(); _ = guest.Close(); <-s.ctl.done })
	args := proto.CtlStartArgs{Session: "child234", RunToken: "run/child234", Agent: "codex", Argv: []string{"codex"}, Env: []string{"HOME=/home/agent"}, Cwd: "/work", Secrets: map[string]string{"OPENAI_API_KEY": "public"}, SecretModes: map[string]api.SecretMode{"OPENAI_API_KEY": api.SecretModeBound}}
	if err := s.planMemberStores([]proto.CtlStartArgs{args}, false, ""); err != nil {
		t.Fatal(err)
	}
	s.storeBatch.allocated, s.storeBatch.acknowledged = true, true
	budget := newActiveBootBudget(parent, 30*time.Second)
	defer budget.close()
	clock.advance(10 * time.Second)
	result := make(chan error, 1)
	go func() { result <- s.prepareMemberPhases(budget, &args, proto.CtlSpawn, true) }()
	reader, writer := bufio.NewReader(peer), proto.NewWriter(peer)
	exchange := func(op string, value any) {
		frame, err := proto.ReadLimit(reader, maxEvent)
		if err != nil {
			t.Fatal(err)
		}
		var request proto.Request
		if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &request) != nil || request.Op != op {
			t.Fatal("unexpected member sub-operation")
		}
		if op == proto.CtlMemberStore {
			budget.mu.Lock()
			paused := budget.paused
			budget.mu.Unlock()
			if paused {
				t.Fatal("duplicate Store received a fresh batch pause")
			}
			clock.advance(2 * time.Second)
		}
		raw, _ := json.Marshal(value)
		if err := writer.WriteJSON(proto.TResponse, proto.Response{ID: request.ID, OK: true, Result: raw}); err != nil {
			t.Fatal(err)
		}
	}
	exchange(proto.CtlGuestCapabilities, proto.CtlGuestFeatures{Version: 1, Features: []string{proto.GuestMemberPrepareV1}})
	exchange(proto.CtlMemberPrepare, proto.CtlMemberPrepareResult{Version: 1, Claim: strings.Repeat("a", 64)})
	exchange(proto.CtlMemberStore, struct{}{})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	deadline, _ := budget.Deadline()
	if deadline.Sub(clock.Now()) != 18*time.Second || !s.storeBatch.allocated || !s.storeBatch.acknowledged {
		t.Fatal("duplicate Store extended/reset remaining active time or lost ACK fact")
	}
}
