package alerts

import "time"

type settlement struct {
	target uint64
	done   chan struct{}
}

// Settle waits for notifications already accepted by Notify to pass their
// actual delay/noise decisions and finish any queued deliveries. It never
// forces a delayed alert early, waits for later notifications, or extends wait.
// False means its budget expired or Close interrupted that work.
func (d *Dispatcher) Settle(wait time.Duration) bool {
	d.mu.Lock()
	target := d.nextSeq
	if d.closed {
		d.mu.Unlock()
		return false
	}
	if d.settledLocked(target) {
		d.mu.Unlock()
		return true
	}
	if wait <= 0 {
		d.mu.Unlock()
		return false
	}
	w := &settlement{target: target, done: make(chan struct{})}
	if d.settling == nil {
		d.settling = make(map[*settlement]bool)
	}
	d.settling[w] = true
	d.mu.Unlock()
	expired := make(chan struct{})
	stop := d.o.Clock.AfterFunc(wait, func() { close(expired) })
	defer stop()
	defer func() { d.mu.Lock(); delete(d.settling, w); d.mu.Unlock() }()
	select {
	case <-w.done:
	case <-expired:
	case <-d.quit:
	}
	d.mu.Lock()
	complete := !d.closed && d.settledLocked(target)
	d.mu.Unlock()
	return complete
}

func (d *Dispatcher) settledLocked(target uint64) bool {
	for seq := range d.unfinished {
		if seq <= target {
			return false
		}
	}
	return true
}

func (d *Dispatcher) complete(seq uint64) {
	if seq == 0 {
		return
	}
	d.mu.Lock()
	d.completeLocked(seq)
	d.mu.Unlock()
}

func (d *Dispatcher) completeLocked(seq uint64) {
	if seq == 0 {
		return
	}
	delete(d.unfinished, seq)
	for waiter := range d.settling {
		if d.settledLocked(waiter.target) {
			delete(d.settling, waiter)
			close(waiter.done)
		}
	}
}
