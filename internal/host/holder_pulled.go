package host

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

type holderPulledArgs struct {
	ID      string   `json:"id"`
	Commits []string `json:"commits"`
}

// afterPulledWrite is the same source-test seam as afterRenameWrite. It runs
// after durable save and before publishing the holder's new in-memory slice.
var afterPulledWrite = func(*Holder) {}

// ApplyPulled clones and validates a commit union for the canonical current
// manifest owner. It changes no status, lifecycle, guest claim or file state.
func ApplyPulled(m *Manifest, commits []string) error {
	for _, oid := range commits {
		if !api.ValidGitOID(oid) {
			return UserError("pull_protocol", "the fetched commit identity is invalid", "Repeat pull through the current client.")
		}
	}
	result := append(slices.Clone(m.Pulled), commits...)
	slices.Sort(result)
	m.Pulled = slices.Compact(result)
	return nil
}

// recordPulled runs under the caller's retained primary op.lock. Reacquiring
// that lease here would deadlock. Full-peer dispatch and original-ID matching
// precede it; the outer manager already checked the sealed export's OIDs.
func (h *Holder) recordPulled(args holderPulledArgs) error {
	if args.ID != h.id {
		return UserError("pull_protocol", "the pulled record names another session", "Repeat pull using the session's full address.")
	}
	h.saveMu.Lock()
	defer h.saveMu.Unlock()
	if h.closed {
		return errEnding
	}
	h.mu.Lock()
	if h.m.path != "" || h.m.Wall != nil {
		h.mu.Unlock()
		return UserError("forbidden", "a guest holder cannot publish trusted pulled state", "Use the trusted supervisor's pull connection.")
	}
	copy := h.copyForSave()
	h.mu.Unlock()
	if err := ApplyPulled(&copy.m, args.Commits); err != nil {
		return err
	}
	if err := copy.m.Save(); err != nil {
		return err
	}
	afterPulledWrite(h)
	h.mu.Lock()
	h.m.Pulled = slices.Clone(copy.m.Pulled)
	h.saved = h.copies
	h.mu.Unlock()
	return nil
}
