package egress

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPrivateGrantChargesOriginalCompleteFrameBeforeMutation(t *testing.T) {
	args := InitArgs{Agent: "shell", Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAsk}, Secrets: map[string]string{"EXPOSED": "secret"}}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	counter := new(privateFrameCounter)
	if err := proto.NewWriter(counter).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: CtlInit, Args: raw}); err != nil {
		t.Fatal(err)
	}
	// Task is ordinary trusted metadata, not a larger secret/scanner fixture.
	// Leave enough of the ORIGINAL frame for one short private target but
	// not a complete maximum-width target and its opaque per-instance data.
	args.JudgeMetadata = classify.Metadata{Task: "x"}
	raw, err = json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	counter = new(privateFrameCounter)
	if err := proto.NewWriter(counter).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: CtlInit, Args: raw}); err != nil {
		t.Fatal(err)
	}
	args.JudgeMetadata.Task = strings.Repeat("x", 1+proto.MaxFrame-(counter.bytes-4)-384)
	server := newMemberServer(t, args)
	longHost := "secret." + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60)
	entry, _ := privateQuestion(t, server, longHost, 443, 0)
	engine, state, ledger := server.currentEngine(), server.currentMembers(), snapshotDNSLedger(server.currentEngine())
	server.policyMu.Lock()
	_, err = server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldOnce, true, "live", Request{Host: longHost, Port: 443})
	server.policyMu.Unlock()
	if err == nil || server.currentEngine() != engine || server.currentMembers() != state || !reflect.DeepEqual(ledger, snapshotDNSLedger(server.currentEngine())) || len(server.pendingNetworkSnapshot().Requests) != 1 {
		t.Fatal("full-state private capacity failure mutated actor/ledger/question")
	}
	short := "secret.example.test"
	entry, _ = privateQuestion(t, server, short, 443, 0)
	server.policyMu.Lock()
	_, err = server.installMemoryGrantLocked(t.Context(), entry, api.NetworkHoldOnce, true, "live", Request{Host: short, Port: 443})
	server.policyMu.Unlock()
	if err != nil {
		t.Fatal("a private rule fitting the original complete frame was refused", err)
	}
	add := MemberAddArgs{Version: 1, Kind: "codex", ExposedValues: map[string]api.Secret{"NEW": api.Secret(strings.Repeat("z", 160))}}
	candidate, _, _, err := server.currentMembers().memberCandidate(add)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareInitialState(candidate); err != nil {
		t.Fatal("source alone did not fit the original bound; fixture does not exercise combined charge", err)
	}
	engine, state, ledger = server.currentEngine(), server.currentMembers(), snapshotDNSLedger(server.currentEngine())
	if _, err := server.addControlMember(memberJSON(t, add)); err == nil {
		t.Fatal("later source addition borrowed private rule bytes beyond the original full state budget")
	}
	if server.currentEngine() != engine || server.currentMembers() != state || !reflect.DeepEqual(ledger, snapshotDNSLedger(server.currentEngine())) {
		t.Fatal("failed combined source/private charge partially installed state")
	}
}
