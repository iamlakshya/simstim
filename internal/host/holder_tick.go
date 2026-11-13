package host

import "time"

// The holder's tick: once a second it reads the session's memory, updates
// the status, and saves the manifest when it changed.

// tick updates the status and saves the manifest when it changed.
func (h *Holder) tick() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := time.Now()
	for range t.C {
		last = h.noteTick(last)
		h.step()
	}
}

// step is one tick: it reads the unit's memory files, updates the status,
// and saves the manifest when it changed.
func (h *Holder) step() {
	mem := h.readMem()
	lt := h.lockTimed()
	on := h.mem.on
	h.checkMemory(mem)
	h.snapshotBefore(on)
	// Before updateStatus: a kill that it claims clears the memory warning's
	// mark, and the status shows that in the same tick.
	if kills := h.checkOOM(mem.kills); kills > 0 {
		h.goSnapshot(kills) // in a goroutine: it reads no file under the lock
	}
	if !h.exited {
		h.updateStatus()
	}
	dirty := h.dirty
	h.dirty = false
	var snapshot savedCopy
	if dirty {
		snapshot = h.copyForSave()
	}
	h.mu.Unlock()
	lt.tick()
	if dirty {
		h.saveTick(snapshot)
	}
}

// saveTick saves a tick's copy. When the save fails, the session stays
// dirty, so the next tick tries again: a failed save loses no change.
func (h *Holder) saveTick(c savedCopy) {
	if err := h.save(c, false); err != nil {
		h.log.Warn("manifest not saved", "err", err)
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
	}
}
