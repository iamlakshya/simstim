package supervisor

import (
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

func journalChangeFixture(t *testing.T) *Supervisor {
	t.Helper()
	id := api.NewSessionID()
	s := &Supervisor{id: id, firstSaved: true, m: &host.Manifest{Schema: 1, ID: id, Name: "journal", Labels: map[string]string{"old": "kept"}}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), rec: record{phase: host.LifeRunning}}
	if err := s.m.Save(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(id)) })
	return s
}

func TestQueuedOldSnapshotCannotEraseDurablyPublishedField(t *testing.T) {
	s := journalChangeFixture(t)
	s.mu.Lock()
	old := s.snapshot()
	s.mu.Unlock()
	var prospective host.Manifest
	s.saveMu.Lock()
	err := s.writeManifestChange(&prospective, false, nil, nil, func(m *host.Manifest) error {
		m.Labels["acknowledged"] = "public-metadata"
		return nil
	}, func(m *host.Manifest) { s.m.Labels = maps.Clone(m.Labels) })
	s.saveMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// This save was queued before the strong acknowledgement, then executes
	// afterwards. The canonical writer must refresh it from live state.
	if err := s.save(&old, false); err != nil {
		t.Fatal(err)
	}
	actual, err := host.LoadManifest(s.id)
	if err != nil || actual.Labels["acknowledged"] != "public-metadata" || actual.Labels["old"] != "kept" {
		t.Fatal("an old queued save erased acknowledged state", err)
	}
}

func TestProspectiveFieldIsNotPublishedBeforeSuccessfulCommit(t *testing.T) {
	s := journalChangeFixture(t)
	original := saveManifest
	defer func() { saveManifest = original }()
	saveManifest = func(*host.Manifest) error { return syscall.EIO }
	published := false
	var prospective host.Manifest
	s.saveMu.Lock()
	err := s.writeManifestChange(&prospective, false, nil, nil, func(m *host.Manifest) error {
		m.Labels["not-committed"] = "public-metadata"
		return nil
	}, func(m *host.Manifest) { published = true; s.m.Labels = maps.Clone(m.Labels) })
	s.saveMu.Unlock()
	if !errors.Is(err, syscall.EIO) || published || s.m.Labels["not-committed"] != "" {
		t.Fatal("failed durability published live metadata or could send an ACK", err)
	}
}
