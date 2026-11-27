package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func abortWire(t *testing.T, target string, args proto.CtlStartArgs) []byte {
	t.Helper()
	raw, err := json.Marshal(proto.CtlMemberPrepareArgs{Version: 1, Target: target, Start: args})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func installAbortFixtureStore(t *testing.T, g *Init, a proto.CtlStartArgs) {
	t.Helper()
	projected := g.preparedMembers[a.Session].projected
	bound, err := memberBoundSentinels(projected)
	if err != nil {
		t.Fatal(err)
	}
	plans, env, err := prepareSecretStores(a.Session, a.Agent, a.Cwd, projected.Env, bound)
	if err != nil || len(plans) != 1 {
		t.Fatal("Store fixture did not select one canonical plan", err)
	}
	g.secretStores = newSecretStoreOwnership()
	if err := g.secretStores.install(g.lifetime, plans[0].name, g.secretStoreInvocation(plans[0], a.Cwd, env), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestPreparedReservationProtectsPrimaryExitAcrossEveryIdleBoundary(t *testing.T) {
	for _, boundary := range []string{"zero-Store", "before-Store", "after-Store"} {
		t.Run(boundary, func(t *testing.T) {
			var g *Init
			var a proto.CtlStartArgs
			if boundary == "zero-Store" {
				g, a = preparedFixture(t)
				a = prepareFixtureMember(t, g, a)
			} else {
				g, a = prepareBoundFixture(t)
			}
			g.members[g.primary].agentEnded = true
			if g.emptyLocked() {
				t.Fatal("primary exit ended Init before the next transaction operation")
			}
			if boundary == "after-Store" {
				installAbortFixtureStore(t, g, a)
				if err := g.storePreparedMember(abortWire(t, proto.CtlSpawn, a)); err != nil {
					t.Fatal(err)
				}
				if g.emptyLocked() {
					t.Fatal("Store completion dropped the final launch reservation")
				}
			}
			outcome, err := g.abortPreparedMember(abortWire(t, proto.CtlSpawn, a))
			if err != nil || outcome.State != "aborted" || !g.emptyLocked() {
				t.Fatal("exact idle abort did not release its own reservation", err)
			}
			if g.commandHolds != 0 || g.preparedMembers[a.Session].state != memberFailed {
				t.Fatal("abort leaked a command hold or removed its tombstone")
			}
			again, err := g.abortPreparedMember(abortWire(t, proto.CtlSpawn, a))
			if err != nil || again.State != "failed" {
				t.Fatal("exact abort was not idempotent", err)
			}
			if boundary == "after-Store" && (g.secretStores.state.claim == nil || !g.secretStores.state.claim.installed) {
				t.Fatal("cleanup erased the installed Store claim")
			}
		})
	}
}
func TestPreparedAbortRawRoleAndKnownClaimMismatchesCannotReleaseReservation(t *testing.T) {
	changes := []func(*proto.CtlStartArgs){func(a *proto.CtlStartArgs) { a.RunToken = "other-run" }, func(a *proto.CtlStartArgs) { a.Cwd = "/other" }, func(a *proto.CtlStartArgs) { a.PreparedClaim = strings.Repeat("c", 64) }}
	for _, change := range changes {
		g, a := preparedFixture(t)
		a = prepareFixtureMember(t, g, a)
		changed := cloneMemberArgs(a)
		change(&changed)
		if _, err := g.abortPreparedMember(abortWire(t, proto.CtlSpawn, changed)); err == nil {
			t.Fatal("changed raw tuple or known claim released reservation")
		}
		if !g.memberStarts[a.Session] || g.preparedMembers[a.Session].state != memberReady {
			t.Fatal("foreign cleanup mutated owned state")
		}
	}
	g, a := preparedFixture(t)
	a = prepareFixtureMember(t, g, a)
	if _, err := g.abortPreparedMember(abortWire(t, proto.CtlStart, a)); err == nil {
		t.Fatal("Start cleanup released a Spawn reservation")
	}
	a.PreparedClaim = "" // lost preparation ACK; only the exact original tuple may clean up
	result, err := g.abortPreparedMember(abortWire(t, proto.CtlSpawn, a))
	if err != nil || result.State != "aborted" {
		t.Fatal("lost preparation ACK could not release exact raw reservation", err)
	}
}
func TestPreparedAbortNeverReleasesConsumedOrRunningStoreReservation(t *testing.T) {
	g, a := preparedFixture(t)
	a = prepareFixtureMember(t, g, a)
	if _, err := g.consumePreparedMember(a, false); err != nil {
		t.Fatal(err)
	}
	result, err := g.abortPreparedMember(abortWire(t, proto.CtlSpawn, a))
	if err != nil || result.State != "consumed" || !g.memberStarts[a.Session] {
		t.Fatal("cleanup released a possibly live final launch", err)
	}
	g, a = prepareBoundFixture(t)
	g.preparedMembers[a.Session].state = memberStoreRunning
	g.commandHolds = 1
	result, err = g.abortPreparedMember(abortWire(t, proto.CtlSpawn, a))
	if err != nil || result.State != "busy" || !g.memberStarts[a.Session] || g.commandHolds != 1 {
		t.Fatal("cleanup claimed a running Store joined", err)
	}
}
func TestSequentialControlQueuesAbortBehindRunningStore(t *testing.T) {
	local, remote := net.Pipe()
	g := New(remote, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	g.sessionPrepared = true
	owner := link.New(local, link.Supervisor, link.Options{})
	go g.Serve()
	t.Cleanup(func() {
		_ = owner.Close()
		go g.end()
		select {
		case <-g.Done():
		case <-time.After(5 * time.Second):
			t.Error("owned guest did not finish")
		}
	})
	a := proto.CtlStartArgs{Session: "child234", RunToken: "run/child234", Agent: "codex", Argv: []string{"codex"}, Env: []string{"HOME=/home/agent"}, Cwd: "/work", Secrets: map[string]string{"OPENAI_API_KEY": "public"}, SecretModes: map[string]api.SecretMode{"OPENAI_API_KEY": api.SecretModeBound}}
	prepared, err := g.prepareMember(abortWire(t, proto.CtlSpawn, a))
	if err != nil {
		t.Fatal(err)
	}
	a.PreparedClaim = prepared.Claim
	g.secretStores = newSecretStoreOwnership()
	g.secretStores.state.gate <- struct{}{}
	defer func() { <-g.secretStores.state.gate }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctl, err := owner.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer, reader := proto.NewWriter(ctl), bufio.NewReader(ctl)
	if err := writer.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: proto.CtlMemberStore, Args: abortWire(t, proto.CtlSpawn, a)}); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		g.mu.Lock()
		running := g.preparedMembers[a.Session].state == memberStoreRunning
		g.mu.Unlock()
		if running {
			break
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatal("Store did not enter its owned gate")
		}
	}
	if err := writer.WriteJSON(proto.TRequest, proto.Request{ID: 2, Op: proto.CtlMemberAbort, Args: abortWire(t, proto.CtlSpawn, a)}); err != nil {
		t.Fatal(err)
	}
	g.cancelLifetime() // joins the blocked Store; it does not dispatch a second ctl reader
	for _, id := range []uint64{1, 2} {
		for {
			frame, err := proto.ReadLimit(reader, maxEvent)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Type == proto.TEvent {
				continue
			}
			var response proto.Response
			if frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id {
				t.Fatal("abort was processed before the running Store joined")
			}
			if id == 2 {
				var result proto.CtlMemberAbortResult
				if !response.OK || json.Unmarshal(response.Result, &result) != nil || result.State != "failed" {
					t.Fatal("queued cleanup lost the joined failure state")
				}
			}
			break
		}
	}
}

func TestAbortedInitialPrimaryReleasesOnlyItsBootAdmission(t *testing.T) {
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &Init{lifetime: owner, cancelLifetime: cancel, root: t.TempDir(), members: map[string]*holderProc{}, memberStarts: map[string]bool{}}
	a := proto.CtlStartArgs{Session: "primary234", RunToken: "run/primary234", Argv: []string{"/bin/true"}, Env: []string{"HOME=/home/agent"}, Cwd: "/work", PendingMembers: 3, SecretModes: map[string]api.SecretMode{}}
	prepared, err := g.prepareMember(abortWire(t, proto.CtlStart, a))
	if err != nil {
		t.Fatal(err)
	}
	if !g.bootAdmission || g.pendingBoot != 3 || g.emptyLocked() {
		t.Fatal("initial primary lost its owned boot barrier")
	}
	a.PreparedClaim = prepared.Claim
	result, err := g.abortPreparedMember(abortWire(t, proto.CtlStart, a))
	if err != nil || result.State != "aborted" || g.bootAdmission || g.pendingBoot != 0 || !g.emptyLocked() {
		t.Fatal("aborted initial primary retained its boot admission", err)
	}
	if len(g.members) != 0 || len(g.owned) != 0 || g.preparedMembers[a.Session].state != memberFailed {
		t.Fatal("initial cleanup launched anything or erased its tombstone")
	}
}
