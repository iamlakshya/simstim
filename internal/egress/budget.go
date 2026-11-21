package egress

import (
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const defaultUploadBudget = 16 << 20

// Upload accounting belongs to the Server, across every Engine replacement.
// These bounded counters carry no network permission. A full hour's entry is
// never evicted to make another host free; pressure requires a held answer.
type uploadBudgets struct {
	mu      sync.Mutex
	hosts   map[string]*uploadWindow
	changed chan struct{}
	closed  bool
}

type uploadWindow struct {
	until          time.Time
	used, reserved int64
}

type uploadReservation struct {
	window *uploadWindow
	bytes  int64
}

func (*uploadBudgets) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func newUploadBudgets() *uploadBudgets {
	return &uploadBudgets{hosts: make(map[string]*uploadWindow), changed: make(chan struct{})}
}

func uploadLimit(policy api.Policy) (int64, error) {
	if policy.Network.UploadBudget == nil {
		return defaultUploadBudget, nil
	}
	limit, err := api.ParseSize(*policy.Network.UploadBudget)
	if err != nil || limit < 0 {
		return 0, badLayerValue("network.upload_budget", "needs a nonnegative byte size")
	}
	return limit, nil
}

func (b *uploadBudgets) windowLocked(host string, now time.Time) *uploadWindow {
	if row := b.hosts[host]; row != nil && (row.reserved != 0 || now.Before(row.until)) {
		return row
	}
	for name, row := range b.hosts {
		if row.reserved == 0 && !now.Before(row.until) {
			delete(b.hosts, name)
		}
	}
	if len(b.hosts) >= MaxSyntheticNames {
		return nil
	}
	row := &uploadWindow{until: now.Add(time.Hour)}
	b.hosts[host] = row
	return row
}

// Reserve before the physical write, commit only its successful prefix.
// In-flight reservations prevent simultaneous flows overshooting the limit.
func (b *uploadBudgets) reserve(host string, length int, limit int64, now time.Time, override bool) (uploadReservation, int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return uploadReservation{}, 0, io.ErrClosedPipe
	}
	row := b.windowLocked(host, now)
	if row == nil {
		if override {
			return uploadReservation{}, length, nil
		}
		return uploadReservation{}, 0, nil
	}
	allowed := int64(length)
	if !override {
		available := limit - min(limit, row.used) - min(limit-min(limit, row.used), row.reserved)
		allowed = min(allowed, available)
	}
	// A reservation itself cannot wrap even under explicit once overrides.
	allowed = min(allowed, math.MaxInt64-row.reserved)
	row.reserved += allowed
	return uploadReservation{window: row, bytes: allowed}, int(allowed), nil
}

func (b *uploadBudgets) finish(reservation uploadReservation, successful int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	row := reservation.window
	if row != nil {
		row.reserved -= reservation.bytes
		row.used += min(int64(successful), math.MaxInt64-row.used)
	}
	if !b.closed {
		close(b.changed)
		b.changed = make(chan struct{})
	}
}

func (b *uploadBudgets) available(host string, limit int64, now time.Time) (bool, <-chan struct{}, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false, b.changed, time.Time{}
	}
	row := b.windowLocked(host, now)
	if row == nil {
		return false, b.changed, time.Time{}
	}
	return row.used < limit && row.reserved < limit-row.used, b.changed, row.until
}

func (b *uploadBudgets) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		clear(b.hosts)
		close(b.changed)
	}
}
