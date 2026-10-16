package host_test

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// capFor is the cap a session that asked for asked bytes gets on this
// machine: never more than the limit of all sessions together, when the
// machine's memory is known (decision 0024).
func capFor(asked int64) int64 {
	limit := host.DefaultMemoryMax()
	if asked == 0 || (limit > 0 && asked > limit) {
		return limit
	}
	return asked
}

// New refuses a cap below the smallest, keeps the size the user asked for,
// and lowers a size above the limit of all sessions together.
func TestNewMemoryCap(t *testing.T) {
	hosttest.New(t)
	_, err := host.New(host.NewArgs{Argv: []string{"sleep", "60"}, MemoryMax: 100 << 20})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "memory_max" || pe.Hint == "" {
		t.Fatalf("new with a cap of 100 MiB = %v, want a memory_max error with a hint", err)
	}
	for _, asked := range []int64{0, api.MinMemoryMax, 1 << 50} {
		s, err := host.New(host.NewArgs{Argv: []string{"sleep", "60"}, MemoryMax: asked})
		if err != nil {
			t.Fatalf("new with a cap of %d: %v", asked, err)
		}
		m := manifest(t, s.ID)
		if m.Limits.MemoryAsked != asked || m.Limits.MemoryMax != capFor(asked) || s.MemoryMax != capFor(asked) {
			t.Errorf("asked for %d: limits %+v, row %d, want the cap %d", asked, m.Limits, s.MemoryMax, capFor(asked))
		}
	}
}

// A session that restore starts again gets its cap worked out again: a
// session from before decision 0024 has no size of its own, so it gets
// today's default, not the cap it was saved with.
func TestRestoreWorksOutTheCapAgain(t *testing.T) {
	hosttest.New(t)
	old := start(t, "old", "sleep", "60")
	own, err := host.New(host.NewArgs{Name: "own", Argv: []string{"sleep", "60"}, MemoryMax: api.MinMemoryMax})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old.ID, own.ID} {
		crash(t, id)
		m := manifest(t, id)
		m.Limits.MemoryMax = 1610612736 // the cap of 0.1.0 on a 4 GB host
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}
	}
	m := manifest(t, old.ID)
	m.Limits.MemoryAsked = 0
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	for _, r := range host.Restore(true) {
		if r.Action != "restored" {
			t.Fatalf("restore: %+v", r)
		}
	}
	if got := manifest(t, old.ID).Limits.MemoryMax; got != capFor(0) {
		t.Errorf("the old session's cap after restore = %d, want the default %d", got, capFor(0))
	}
	if got := manifest(t, own.ID).Limits.MemoryMax; got != capFor(api.MinMemoryMax) {
		t.Errorf("the cap the user asked for after restore = %d, want %d", got, capFor(api.MinMemoryMax))
	}
}
