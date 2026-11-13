package host

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// testHolderID is the session of the holders of these tests.
const testHolderID = "abcd2345"

// newTestHolder is a holder of the session testHolderID, named a, with no process:
// enough for its saves and its rename.
func newTestHolder(t *testing.T) *Holder {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	h := &Holder{id: testHolderID, m: &Manifest{ID: testHolderID, Name: "a"}}
	if err := h.m.Save(); err != nil {
		t.Fatal(err)
	}
	return h
}

// failSaves makes every save of the session's manifest fail, until the
// test lets it go: a folder with a file in it stands where the manifest
// goes, so the save's rename fails.
func failSaves(t *testing.T) func() {
	t.Helper()
	p := paths.Manifest(testHolderID)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p, "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
}

// The holder numbers each copy of the manifest that it saves. A copy taken
// before a rename, and saved after it, never puts the old name back
// (docs/contributing/cli-spec.md 9.5).
func TestAnOlderCopyNeverReplacesANewerOne(t *testing.T) {
	h := newTestHolder(t)
	older := h.copyForSave()
	h.m.Name = "b"
	newer := h.copyForSave()
	if saved, err := h.write(newer, false); !saved || err != nil {
		t.Fatalf("the newer copy: %v, %v", saved, err)
	}
	if err := h.save(older, false); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(testHolderID)
	if err != nil || m.Name != "b" {
		t.Errorf("after an older copy's save, the manifest is %+v, %v", m, err)
	}
	// After the last save, a write says that the holder could not save.
	if err := h.save(h.copyForSave(), true); err != nil {
		t.Fatal(err)
	}
	if saved, _ := h.write(h.copyForSave(), false); saved {
		t.Error("a write after the last save says that it saved")
	}
}

// The saved number moves only after a good write (V1): a newer copy whose
// write fails does not make an older copy, such as a rename's, count as
// saved, so the older copy is written.
func TestAFailedWriteSavesNothing(t *testing.T) {
	h := newTestHolder(t)
	renamed := h.copyForSave()
	renamed.m.Name = "b"
	newer := h.copyForSave()
	release := failSaves(t)
	if _, err := h.write(newer, false); err == nil {
		t.Fatal("a write that must fail worked")
	}
	release()
	if _, err := h.write(renamed, false); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadManifest(testHolderID); err != nil || m.Name != "b" {
		t.Errorf("the rename's copy was not written: %+v, %v", m, err)
	}
}

// The holder's rename answers yes only once the manifest holds the new
// name. A failed write leaves the name in memory, and the saved number, as
// they were, and is the error (V1).
func TestTheHoldersRenameWritesFirst(t *testing.T) {
	h := newTestHolder(t)
	release := failSaves(t)
	if err := h.rename("b"); err == nil {
		t.Fatal("a rename that could not write answered yes")
	}
	release()
	if h.m.Name != "a" || h.saved != 0 {
		t.Errorf("after a failed rename: the name %q, saved %d", h.m.Name, h.saved)
	}
	if err := h.rename("b"); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadManifest(testHolderID); err != nil || m.Name != "b" || h.m.Name != "b" {
		t.Errorf("after a rename: the manifest %+v, %v, and the name %q", m, err, h.m.Name)
	}
}

// A copy that a tick takes during a rename's write holds the old name. It
// is older than the rename once the rename is done, so its save writes the
// session as it is now, with the new name.
func TestATickDuringARenameKeepsTheNewName(t *testing.T) {
	h := newTestHolder(t)
	var tick savedCopy
	old := afterRenameWrite
	t.Cleanup(func() { afterRenameWrite = old })
	afterRenameWrite = func(h *Holder) {
		h.mu.Lock()
		tick = h.copyForSave()
		h.mu.Unlock()
	}
	if err := h.rename("b"); err != nil {
		t.Fatal(err)
	}
	if tick.m.Name != "a" {
		t.Fatalf("the tick's copy has the name %q", tick.m.Name)
	}
	if err := h.save(tick, false); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadManifest(testHolderID); err != nil || m.Name != "b" {
		t.Errorf("a tick's copy from during the rename put back %+v, %v", m, err)
	}
}

// answerSpy is a client's connection that reads the manifest from disk
// at the moment the answer arrives.
type answerSpy struct{ names []string }

func (a *answerSpy) Write(b []byte) (int, error) {
	name := "(none)"
	if m, err := LoadManifest(testHolderID); err == nil {
		name = m.Name
	}
	a.names = append(a.names, name)
	return len(b), nil
}

// The rename's answer comes after its write, with no timing in the test
// (S2): when the answer reaches the client, the manifest has the new name.
func TestTheRenameAnswersAfterItWrites(t *testing.T) {
	h := newTestHolder(t)
	spy := &answerSpy{}
	c := &hclient{w: proto.NewWriter(spy)}
	args, _ := json.Marshal(map[string]string{"name": "b"})
	h.request(c, proto.Request{ID: 1, Op: "rename", Args: args})
	if len(spy.names) != 1 || spy.names[0] != "b" {
		t.Errorf("when the answer arrived, the manifest's name was %q, want b", spy.names)
	}
}

// A failed tick save keeps the session dirty, and does not stand in the way
// of a rename (S3): the rename answers yes only once the manifest holds the
// new name.
func TestAFailedTickThenARename(t *testing.T) {
	h := newTestHolder(t)
	h.m.Agent.Kind = "shell" // a change for the tick to save
	h.dirty = true
	tick := h.copyForSave()
	h.dirty = false
	h.log = slog.New(slog.DiscardHandler)
	release := failSaves(t)
	h.saveTick(tick)
	release()
	if !h.dirty {
		t.Error("a failed tick save left the session clean")
	}
	if err := h.rename("b"); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadManifest(testHolderID); err != nil || m.Name != "b" {
		t.Errorf("the rename answered yes, and the manifest is %+v, %v", m, err)
	}
}

// A tick save that keeps failing is tried again at each tick, and leaves
// no temporary file behind (review 4, finding 5).
func TestFailedTicksLeaveNoTemporaryFile(t *testing.T) {
	h := newTestHolder(t)
	h.log = slog.New(slog.DiscardHandler)
	release := failSaves(t)
	defer release()
	for range 5 {
		h.mu.Lock()
		h.dirty = false
		c := h.copyForSave()
		h.mu.Unlock()
		h.saveTick(c)
	}
	left, err := filepath.Glob(filepath.Join(paths.SessionDir(testHolderID), ".manifest-*"))
	if err != nil || len(left) != 0 || !h.dirty {
		t.Errorf("after 5 failed ticks: dirty %v, temporary files %v, %v", h.dirty, left, err)
	}
}

// A rename whose write fails keeps the old name in memory, so no later
// save, such as the last one, writes the new name (S4).
func TestAFailedRenameNeverLandsLater(t *testing.T) {
	h := newTestHolder(t)
	release := failSaves(t)
	if err := h.rename("b"); err == nil {
		t.Fatal("a rename that could not write answered yes")
	}
	release()
	if err := h.save(h.copyForSave(), true); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadManifest(testHolderID); err != nil || m.Name != "a" {
		t.Errorf("after a failed rename and the last save, the manifest is %+v, %v", m, err)
	}
}

// The last save, with a copy taken during a rename's write, holds the new
// name, so the rename's yes matches the manifest (S4). A rename after the
// last save says that the session is ending, and the manifest keeps the old
// name.
func TestTheLastSaveAndARename(t *testing.T) {
	h := newTestHolder(t)
	var last savedCopy
	old := afterRenameWrite
	t.Cleanup(func() { afterRenameWrite = old })
	afterRenameWrite = func(h *Holder) {
		h.mu.Lock()
		last = h.copyForSave()
		h.mu.Unlock()
	}
	if err := h.rename("b"); err != nil {
		t.Fatal(err)
	}
	if saved, err := h.write(last, true); !saved || err != nil {
		t.Fatalf("the last save: %v, %v", saved, err)
	}
	if m, err := LoadManifest(testHolderID); err != nil || m.Name != "b" {
		t.Errorf("the rename answered yes, and after the last save the manifest is %+v, %v", m, err)
	}

	afterRenameWrite = old
	h2 := newTestHolder(t)
	if err := h2.save(h2.copyForSave(), true); err != nil {
		t.Fatal(err)
	}
	if err := h2.rename("b"); err == nil {
		t.Error("a rename after the last save answered yes")
	}
	if m, err := LoadManifest(testHolderID); err != nil || m.Name != "a" {
		t.Errorf("after a refused rename, the manifest is %+v, %v", m, err)
	}
}
