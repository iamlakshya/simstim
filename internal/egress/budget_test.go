package egress

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestUploadBudgetReservesConcurrentWritesAndCommitsOnlySuccessfulPrefixes(t *testing.T) {
	budget := newUploadBudgets()
	now := time.Now()
	var workers sync.WaitGroup
	var accepted atomic.Int64
	var mu sync.Mutex
	var reservations []uploadReservation
	for range maxActiveFlows {
		workers.Go(func() {
			reservation, count, err := budget.reserve("paste.example", 1<<20, defaultUploadBudget, now, false)
			if err != nil {
				t.Error(err)
				return
			}
			accepted.Add(int64(count))
			mu.Lock()
			reservations = append(reservations, reservation)
			mu.Unlock()
		})
	}
	workers.Wait()
	if accepted.Load() != defaultUploadBudget {
		t.Fatal("concurrent physical reservations overshot or lost byte capacity")
	}
	// One admitted MiB write fails after only three successful bytes. Its
	// unwritten suffix returns to the same host's budget exactly once.
	failed := false
	for _, reservation := range reservations {
		count := int(reservation.bytes)
		if !failed && count > 0 {
			count, failed = 3, true
		}
		budget.finish(reservation, count)
	}
	reservation, count, err := budget.reserve("paste.example", 1<<20, defaultUploadBudget, now, false)
	if err != nil || count != (1<<20)-3 {
		t.Fatal("failed physical write spent buffered bytes or lost its reservation", err)
	}
	budget.finish(reservation, count)
	if available, _, _ := budget.available("paste.example", defaultUploadBudget, now); available {
		t.Fatal("a spent physical budget became free")
	}
	if available, _, _ := budget.available("paste.example", 8<<20, now); available {
		t.Fatal("lowering the limit reset already spent bytes")
	}
	if available, _, _ := budget.available("paste.example", 32<<20, now); !available {
		t.Fatal("the current raised limit did not retain and subtract spent bytes")
	}
	if available, _, _ := budget.available("paste.example", defaultUploadBudget, now.Add(-time.Hour)); available {
		t.Fatal("a backwards clock reset the physical byte window")
	}
	if available, _, _ := budget.available("paste.example", defaultUploadBudget, now.Add(time.Hour)); !available {
		t.Fatal("a completed hour retained the former byte charge")
	}
	zero, err := uploadLimit(api.Policy{LayerValues: api.LayerValues{Network: api.NetworkValues{UploadBudget: new("0M")}}})
	if err != nil || zero != 0 {
		t.Fatal("typed zero byte-size semantics changed", err)
	}
}

func TestStricterTargetPipelineRejectsPrivateAndPublicSuffixWildcards(t *testing.T) {
	for _, target := range []string{"*.vercel.app", "*.com", "*.co.uk", "*.github.io"} {
		if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: target}}}}); err == nil {
			t.Fatalf("actual policy source accepted shared suffix %q", target)
		}
	}
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: "app.vercel.app"}}}})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	if !engine.Decide(Request{Host: "app.vercel.app", Port: 443}).Allow || engine.Decide(Request{Host: "other.vercel.app", Port: 443}).Allow {
		t.Fatal("the exact private-suffix tenant opened another owner's host")
	}
}
