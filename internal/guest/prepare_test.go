package guest

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestControllerPreparationStartsInfrastructureWithoutPrimaryProcess(t *testing.T) {
	g := &Init{root: t.TempDir(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	args := proto.CtlPrepareArgs{Version: 1, Start: proto.CtlStartArgs{Session: "a2a2a2a2", Cwd: "/work", Env: []string{"PATH=/usr/bin:/bin"}, PendingMembers: 1}}
	raw, _ := json.Marshal(args)
	result, err := g.prepareController(raw)
	if err != nil || result.Version != 1 || result.GuestABI != g.ptyABI() {
		t.Fatal("canonical infrastructure preparation failed", err)
	}
	if !g.sessionPrepared || g.holder != nil || len(g.members) != 0 || len(g.owned) != 0 || g.primary != args.Start.Session || g.execCwd != args.Start.Cwd {
		t.Fatal("preparation launched a dummy primary or lost its controller identity")
	}
	g.mu.Lock()
	if g.emptyLocked() {
		t.Fatal("pending selected children lost their boot hold")
	}
	done := make(chan struct{})
	close(done)
	g.pendingBoot = 0
	g.members = map[string]*holderProc{"b3b3b3b3": {id: "b3b3b3b3", agentEnded: true, done: done}}
	if g.emptyLocked() {
		t.Fatal("fast final child escaped before the controller's ready transaction")
	}
	g.bootAdmission = false // canonical CtlMembersReady commits this transaction
	if !g.emptyLocked() {
		t.Fatal("last selected child could not naturally empty a controller with no primary process")
	}
	g.mu.Unlock()
}

func TestControllerPreparationRejectsLaunchValuesAndAmbiguousFieldsBeforeMutation(t *testing.T) {
	args := proto.CtlPrepareArgs{Version: 1, Start: proto.CtlStartArgs{Session: "a2a2a2a2", Cwd: "/work", PendingMembers: 1}}
	raw, _ := json.Marshal(args)
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1),
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"Version":1`), 1),
		bytes.Replace(raw, []byte(`"pending_members":1`), []byte(`"pending_members":0`), 1),
		bytes.Replace(raw, []byte(`"argv":null`), []byte(`"argv":["/bin/sh"]`), 1),
		bytes.Replace(raw, []byte(`"session":"a2a2a2a2"`), []byte(`"session":"a2a2a2a2","Session":"b3b3b3b3"`), 1),
		bytes.Replace(raw, []byte(`"cwd":"/work"`), []byte(`"cwd":"/work","secrets":{"TOKEN":"never-fetched"}`), 1),
		bytes.Replace(raw, []byte(`"cwd":"/work"`), []byte(`"cwd":"/work","run_token":"not-an-agent"`), 1),
	} {
		g := &Init{root: t.TempDir()}
		if _, err := g.prepareController(bad); err == nil || g.primary != "" || g.sessionPrepared || g.starting || len(g.owned) != 0 {
			t.Fatal("invalid controller preparation mutated or started a member")
		}
	}
}
