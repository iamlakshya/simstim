package egress

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type memberCASEntropyReader struct{ reads atomic.Uint64 }

func (r *memberCASEntropyReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	clear(p)
	return len(p), nil
}

func TestBoundMemberCASRefusesMissingAndStaleBeforeEntropyOrAnyMutation(t *testing.T) {
	const childFlag = "GO_MEMBER_CAS_ENTROPY_CHILD"
	if os.Getenv(childFlag) != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBoundMemberCASRefusesMissingAndStaleBeforeEntropyOrAnyMutation$")
		root := t.TempDir()
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if name == "HOME" || name == "USERPROFILE" || name == "GNUPGHOME" || name == "GIT_CONFIG_GLOBAL" || name == "GIT_CONFIG_SYSTEM" || name == childFlag || strings.HasPrefix(name, "XDG_") {
				continue
			}
			command.Env = append(command.Env, entry)
		}
		command.Env = append(command.Env, childFlag+"=1", "HOME="+root, "USERPROFILE="+root, "GNUPGHOME="+filepath.Join(root, "gnupg"), "GIT_CONFIG_GLOBAL="+filepath.Join(root, "git-global"), "GIT_CONFIG_SYSTEM="+filepath.Join(root, "git-system"))
		for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
			command.Env = append(command.Env, name+"="+filepath.Join(root, name))
		}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated membership CAS entropy witness failed: %v\n%s", err, output)
		}
		return
	}
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	owner := boundRequestScope(t, server, []string{"KEY"})
	before, engine, redaction := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	reader := new(memberCASEntropyReader)
	previous := rand.Reader
	rand.Reader = reader
	defer func() { rand.Reader = previous }()
	for _, generation := range []string{"", "stale", initial.Generation + "changed"} {
		for _, kind := range []string{"codex", "unknown-prospective-kind"} {
			_, err := server.addBoundControlMember(memberJSON(t, memberIntent(generation, nil, kind)))
			requireBoundError(t, err, "secret_generation")
			if reader.reads.Load() != 0 || server.currentMembers() != before || server.currentEngine() != engine || server.scrubber.snapshot() != redaction || owner.ctx.Err() != nil {
				t.Fatal("member CAS refusal consumed entropy, prepared/published state or disturbed an active owner")
			}
		}
	}
}

func TestBoundMemberCASQueuedBehindArmRejectsTheActuallyStaleSnapshot(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	owner := boundRequestScope(t, server, []string{"KEY"})
	stale := memberIntent(initial.Generation, map[string]api.Secret{"KEY": "EFGH2222", "LATE": "QRST4444"}, "")
	armed := make(chan ArmResult, 1)
	armErr := make(chan error, 1)
	go func() {
		result, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}}))
		armed <- result
		armErr <- err
	}()
	select {
	case <-owner.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("arm did not hold the real registry gate while joining its owner")
	}
	started, memberDone := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := server.addBoundControlMember(memberJSON(t, stale))
		memberDone <- err
	}()
	<-started
	select {
	case <-memberDone:
		t.Fatal("queued member passed the gate before the arm owner completed")
	default:
	}
	owner.close()
	var result ArmResult
	select {
	case result = <-armed:
	case <-time.After(2 * time.Second):
		t.Fatal("actual owner completion did not release arm")
	}
	if err := <-armErr; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-memberDone:
		requireBoundError(t, err, "secret_generation")
	case <-time.After(2 * time.Second):
		t.Fatal("queued stale member did not complete after arm")
	}
	current := server.currentMembers()
	if current.generation != result.Generation || current.secrets["KEY"].value != "WXYZ1111" || current.secrets["KEY"].bound.sentinel != initial.Secrets["KEY"].Sentinel {
		t.Fatal("stale member replaced the winner's value, authority or live stand-in")
	}
	if _, present := current.secrets["LATE"]; present {
		t.Fatal("stale member partially admitted a new name")
	}
}

func TestBoundConcurrentMembersCannotBothSpendOneAuthoritySnapshot(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	args := []MemberAddV2Args{memberIntent(initial.Generation, map[string]api.Secret{"FIRST": "EFGH2222"}, ""), memberIntent(initial.Generation, map[string]api.Secret{"SECOND": "QRST4444"}, "")}
	server.policyMu.Lock()
	started := make(chan struct{}, 2)
	done := make(chan error, 2)
	for _, arg := range args {
		go func() { started <- struct{}{}; _, err := server.addBoundControlMember(memberJSON(t, arg)); done <- err }()
	}
	<-started
	<-started
	server.policyMu.Unlock()
	winners := 0
	for range args {
		select {
		case err := <-done:
			if err == nil {
				winners++
			} else {
				requireBoundError(t, err, "secret_generation")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent membership transaction did not leave the gate")
		}
	}
	if winners != 1 || len(server.currentMembers().secrets) != 2 {
		t.Fatal("one approved authority snapshot admitted multiple distinct memberships")
	}
}

func TestBoundMemberCASExactSnapshotKeepsIdempotentAliasAndRequiredJSONField(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"A": "ABCD0000"}))
	owner := boundRequestScope(t, server, []string{"A"})
	result, err := server.addBoundControlMember(memberJSON(t, memberIntent(initial.Generation, map[string]api.Secret{"B": "ABCD0000"}, "")))
	if err != nil || result.Generation == initial.Generation || result.Env["B"] != initial.Secrets["A"].Sentinel || owner.ctx.Err() != nil {
		t.Fatal("exact authority CAS changed compatible alias or active request behaviour")
	}
	before, engine, redaction := server.currentMembers(), server.currentEngine(), server.scrubber.snapshot()
	if _, err := server.addBoundControlMember(memberJSON(t, memberIntent(result.Generation, map[string]api.Secret{"B": "ABCD0000"}, ""))); err != nil || before != server.currentMembers() || engine != server.currentEngine() || redaction != server.scrubber.snapshot() {
		t.Fatal("exact idempotent member retry changed existing invariants")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(memberJSON(t, MemberAddV2Args{Version: 2}), &object) != nil {
		t.Fatal("cannot inspect membership DTO")
	}
	if raw, present := object["generation"]; !present || string(raw) != `""` {
		t.Fatal("required membership CAS field was silently omitted")
	}
}

func TestBoundMemberCASDoesNotChangeExplicitV1Membership(t *testing.T) {
	server := newMemberServer(t, InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked}})
	result, err := server.addVersionedControlMember(memberJSON(t, MemberAddArgs{Version: MemberRegistryV1, Kind: "codex", ExposedValues: map[string]api.Secret{"KEY": "explicit-legacy-value"}}))
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok := result.(MemberAddResult)
	if !ok || legacy.Version != MemberRegistryV1 || legacy.Env["KEY"] != "explicit-legacy-value" || server.currentMembers().version != MemberRegistryV1 {
		t.Fatal("required v2 CAS changed the explicit legacy handler or projected bound state through it")
	}
}
