package supervisor

import (
	"errors"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
)

func accountJournalFixtureV1(t *testing.T) *Supervisor {
	t.Helper()
	s := journalChangeFixture(t)
	s.generation = strings.Repeat("a", 32)
	s.rec.started = time.Unix(1771234567, 0)
	s.m.Wall = &host.WallRecord{Level: "microvm", Driver: "chv"}
	s.m.Run.Generation, s.m.Run.BootID = s.generation, host.BootID()
	s.m.Run.StartedAt = s.rec.started
	return s
}

// These are real canonical journal fixtures for descriptive DATA. They do
// not mint the original runtime/OP or claim a successful native account boot.
func TestNativeAccountJournalSurvivesQueuedSaveThenRetiresAtStop(t *testing.T) {
	s := accountJournalFixtureV1(t)
	s.mu.Lock()
	old := s.snapshot()
	s.mu.Unlock()
	var prospective host.Manifest
	s.saveMu.Lock()
	err := s.writeManifestChange(&prospective, false, nil, nil, func(m *host.Manifest) error {
		m.Run.NativeAccountMode, m.Run.NativeAccountGeneration = host.NativeAccountOwnV1, s.generation
		return nil
	}, func(m *host.Manifest) {
		s.m.Run.NativeAccountMode, s.m.Run.NativeAccountGeneration = m.Run.NativeAccountMode, m.Run.NativeAccountGeneration
	})
	s.saveMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.save(&old, false); err != nil {
		t.Fatal(err)
	}
	actual, err := host.LoadManifest(s.id)
	if err != nil || actual.Run.NativeAccountMode != host.NativeAccountOwnV1 || actual.Run.NativeAccountGeneration != s.generation {
		t.Fatal("queued metadata save erased the committed current-run description:", err)
	}
	s.mu.Lock()
	s.stopIntent = true
	s.mu.Unlock()
	if err := s.save(&old, false); err != nil {
		t.Fatal(err)
	}
	actual, err = host.LoadManifest(s.id)
	if err != nil || actual.Run.NativeAccountMode != "" || actual.Run.NativeAccountGeneration != "" {
		t.Fatal("an old queued snapshot revived a stopped run's descriptive label:", err)
	}
}

func TestNativeAccountFailedCommitPublishesNoLiveDescription(t *testing.T) {
	s := accountJournalFixtureV1(t)
	original := saveManifest
	defer func() { saveManifest = original }()
	saveManifest = func(*host.Manifest) error { return syscall.EIO }
	var prospective host.Manifest
	published := false
	s.saveMu.Lock()
	err := s.writeManifestChange(&prospective, false, nil, nil, func(m *host.Manifest) error {
		m.Run.NativeAccountMode, m.Run.NativeAccountGeneration = host.NativeAccountOwnV1, s.generation
		return nil
	}, func(m *host.Manifest) {
		published = true
		s.m.Run.NativeAccountMode, s.m.Run.NativeAccountGeneration = m.Run.NativeAccountMode, m.Run.NativeAccountGeneration
	})
	s.saveMu.Unlock()
	if !errors.Is(err, syscall.EIO) || published || s.m.Run.NativeAccountMode != "" {
		t.Fatal("failed durability published a positive live description")
	}
}

func TestNativeAccountReadyCannotBePublishedWithoutOriginalIssuer(t *testing.T) {
	s := accountJournalFixtureV1(t)
	if err := s.publishNativeAccountReadyV1(t.Context()); err == nil || s.m.Run.NativeAccountMode != "" {
		t.Fatal("saved run metadata issued its own native account display source")
	}
	for _, phase := range []string{host.LifeBooting, host.LifeEnding, host.LifeStopped, host.LifeEnded} {
		s.rec.phase = phase
		m := host.CloneManifest(*s.m)
		m.Run.NativeAccountMode, m.Run.NativeAccountGeneration = host.NativeAccountOwnV1, s.generation
		s.retireNativeAccountV1(&m)
		if m.Run.NativeAccountMode != "" || m.Run.NativeAccountGeneration != "" {
			t.Fatal("a non-running phase retained the previous account mark")
		}
	}
}

func TestDelayedNativeObservationCannotReinstateStopBeforeEdit(t *testing.T) {
	s := accountJournalFixtureV1(t)
	// A genuine native observation may finish before it obtains the journal
	// turn. Delay that turn and deliver the ordinary real stop intent first.
	s.saveMu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.commitNativeAccountDisplayV1(t.Context(), s.generation, true) }()
	s.markStopIntent()
	s.saveMu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, errStopped) {
			t.Fatal("retired observation did not refuse ready publication:", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed descriptive journal did not join")
	}
	actual, err := host.LoadManifest(s.id)
	if err != nil || actual.Run.NativeAccountMode != "" || actual.Run.NativeAccountGeneration != "" || s.m.Run.NativeAccountMode != "" {
		t.Fatal("the pre-stop captured observation reinstated a live or durable marker:", err)
	}
}

func TestStopDuringActualJournalIOCannotPublishRetiredObservation(t *testing.T) {
	s := accountJournalFixtureV1(t)
	original := saveManifest
	defer func() { saveManifest = original }()
	entered, release := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	saveManifest = func(m *host.Manifest) error {
		if writes.Add(1) == 1 {
			if m.Run.NativeAccountMode != host.NativeAccountOwnV1 {
				t.Error("fixture did not reach the actual positive prospective save")
			}
			close(entered)
			<-release
		}
		return original(m)
	}
	done := make(chan error, 1)
	go func() { done <- s.commitNativeAccountDisplayV1(t.Context(), s.generation, true) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the canonical writer did not reach its prospective disk IO")
	}
	// Both transitions happen after the snapshot AND edit, while actual disk
	// IO is outside mu. The post-durability publish must re-check them.
	s.markStopIntent()
	s.ending()
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, errStopped) {
			t.Fatal("a retired ready publication was acknowledged:", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupted journal turn did not join")
	}
	actual, err := host.LoadManifest(s.id)
	if err != nil || actual.Run.NativeAccountMode != "" || actual.Run.NativeAccountGeneration != "" || actual.Life.Phase != host.LifeEnding {
		t.Fatal("the same journal turn retained the stopped generation's durable marker:", err)
	}
	if s.m.Run.NativeAccountMode != "" || writes.Load() < 2 {
		t.Fatal("retired data reached live publication or lacked its same-turn correction")
	}
}
