package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPulledRecordSurvivesTickTakenBeforeAndDuringSave(t *testing.T) {
	h := newTestHolder(t)
	older := h.copyForSave()
	var during savedCopy
	previous := afterPulledWrite
	t.Cleanup(func() { afterPulledWrite = previous })
	afterPulledWrite = func(h *Holder) { h.mu.Lock(); during = h.copyForSave(); h.mu.Unlock() }
	oid := strings.Repeat("b", 40)
	unlock, err := lockSessionWait(h.id, renameWait)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.recordPulled(holderPulledArgs{ID: h.id, Commits: []string{oid}}); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock() // no nested lease was needed by the holder
	for _, copy := range []savedCopy{older, during} {
		if err := h.save(copy, false); err != nil {
			t.Fatal(err)
		}
		m, err := LoadManifest(h.id)
		if err != nil || !slices.Equal(m.Pulled, []string{oid}) {
			t.Fatal("a stale tick lost acknowledged work", m, err)
		}
	}
}

func TestFailedPulledSavePublishesNoMemoryOrSavedCounter(t *testing.T) {
	h := newTestHolder(t)
	h.m.Pulled = []string{strings.Repeat("a", 40)}
	before := slices.Clone(h.m.Pulled)
	release := failSaves(t)
	if err := h.recordPulled(holderPulledArgs{ID: h.id, Commits: []string{strings.Repeat("b", 40)}}); err == nil {
		t.Fatal("failed durable save answered yes")
	}
	release()
	if !slices.Equal(h.m.Pulled, before) || h.saved != 0 {
		t.Fatal("failed save changed the authoritative in-memory record")
	}
}

func TestPulledRecordRefusesLimitedPeerWrongIDAndGuestFile(t *testing.T) {
	h := newTestHolder(t)
	args := holderPulledArgs{ID: h.id, Commits: []string{strings.Repeat("a", 40)}}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range []access{accessNone, accessHook, accessPing} {
		var output bytes.Buffer
		h.request(&hclient{access: access, w: proto.NewWriter(&output)}, proto.Request{ID: 1, Op: "pull.record", Args: data})
		frame, err := proto.Read(bufio.NewReader(&output))
		var response proto.Response
		if err != nil || json.Unmarshal(frame.Payload, &response) != nil || response.OK || response.Error == nil || response.Error.Code != "forbidden" {
			t.Fatal("limited peer reached the trusted pulled writer", response, err)
		}
	}
	if err := h.recordPulled(holderPulledArgs{ID: "another2", Commits: args.Commits}); err == nil {
		t.Fatal("another session's identity published Pulled")
	}
	h.m.path = "guest-holder.json"
	if err := h.recordPulled(args); err == nil {
		t.Fatal("guest-file manifest published trusted Pulled")
	}
	if len(h.m.Pulled) != 0 {
		t.Fatal("refused record changed memory")
	}
}
