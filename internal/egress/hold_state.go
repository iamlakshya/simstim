package egress

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type networkHoldKey struct {
	host         string
	port         int
	uploadBudget bool
}

type networkHold struct {
	originalHost string
	meta         api.PendingNetworkRequest
	request      Request
	waiters      map[uint64]*Permit
	done         chan struct{}
	reason       string
}

// This is pending workflow state, never another permission or secret ledger.
// Its leaf lock is never held while acquiring policy, a permit, I/O or RPC.
type networkHolds struct {
	scrubber           *Scrubber
	mu                 sync.Mutex
	entries            map[networkHoldKey]*networkHold
	byID               map[string]*networkHold
	changed            chan struct{}
	next               uint64
	closed             bool
	entropy            io.Reader
	refusals           []time.Time
	revision           uint64
	classifierRefusals map[networkHoldKey]time.Time
}

type networkHoldTicket struct {
	entry   *networkHold
	waiter  uint64
	changed <-chan struct{}
}

type networkHoldChange struct {
	Revision uint64
	Reason   string
}

var (
	errHoldPressure = errors.New("pending network request capacity is full. Try: answer existing network requests before opening another host")
	errHoldEnded    = errors.New("pending network request has ended. Try: retry the request against current policy")
	errHoldEntropy  = errors.New("cannot create a pending network request. Try: check host entropy and update matching helpers")
	errHoldRedactor = errors.New("pending network display has no owned redactor. Try: update matching helpers")
)

func newNetworkHolds(scrubber *Scrubber) (*networkHolds, error) {
	if scrubber == nil {
		return nil, errHoldRedactor
	}
	return &networkHolds{scrubber: scrubber, entries: make(map[networkHoldKey]*networkHold), byID: make(map[string]*networkHold), changed: make(chan struct{}), entropy: rand.Reader, revision: 1, classifierRefusals: make(map[networkHoldKey]time.Time)}, nil
}

// publicView uses the same source-bound redactor at every publication. The
// original target stays private; masked display is never grant input.
func (h *networkHolds) publicView(entry *networkHold) api.PendingNetworkRequest {
	result := entry.meta
	result.Port = entry.request.Port
	result.Host, result.Redacted = publicNetworkHost(h.scrubber, entry.request.Host, entry.originalHost)
	return result
}

func publicNetworkHost(scrubber *Scrubber, canonical, original string) (string, bool) {
	secrets := scrubber.snapshot()
	display := cleanAuditText(canonical, secrets, 253)
	redacted := display != canonical
	if host, _, err := net.SplitHostPort(original); err == nil {
		original = host
	}
	if original != "" {
		masked := cleanAuditText(original, secrets, 253)
		if masked != original {
			display, redacted = masked, true
		}
	}
	return display, redacted
}

// The caller holds the existing policy gate, after its same-matcher precheck.
// Joining never extends the original expiry or consumes another pending slot.
func (h *networkHolds) attach(request Request, originalHost string, now time.Time, openFor time.Duration) (networkHoldTicket, []networkHoldChange, error) {
	return h.join(request, originalHost, now, openFor, true)
}

func (h *networkHolds) retain(request Request, originalHost string, now time.Time, openFor time.Duration) (networkHoldTicket, []networkHoldChange, error) {
	return h.join(request, originalHost, now, openFor, false)
}

func (h *networkHolds) join(request Request, originalHost string, now time.Time, openFor time.Duration, waiting bool) (networkHoldTicket, []networkHoldChange, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changes := h.expireLocked(now)
	if h.closed || openFor <= 0 {
		return networkHoldTicket{}, changes, errHoldEnded
	}
	canonical, err := NormaliseHost(request.Host)
	if err != nil || canonical != request.Host || request.Port < 1 || request.Port > 65535 {
		return networkHoldTicket{}, changes, errHoldEnded
	}
	if h.next == ^uint64(0) {
		return networkHoldTicket{}, changes, errHoldEnded
	}
	key := networkHoldKey{host: request.Host, port: request.Port, uploadBudget: request.uploadBudgetOnly}
	entry := h.entries[key]
	if entry == nil {
		if len(h.entries) >= maxActiveFlows {
			return networkHoldTicket{}, changes, errHoldPressure
		}
		var entropy [16]byte
		if _, err := io.ReadFull(h.entropy, entropy[:]); err != nil {
			return networkHoldTicket{}, changes, errHoldEntropy
		}
		id := hex.EncodeToString(entropy[:])
		if _, duplicate := h.byID[id]; duplicate {
			return networkHoldTicket{}, changes, errHoldEntropy
		}
		entry = &networkHold{originalHost: originalHost, meta: api.PendingNetworkRequest{Version: api.NetworkHoldV1, ID: id, Revision: 1, Opened: now.UTC(), Expires: now.Add(openFor).UTC(), Status: api.NetworkHoldOpen},
			request: Request{Host: request.Host, Port: request.Port, uploadBudgetOnly: request.uploadBudgetOnly}, waiters: make(map[uint64]*Permit), done: make(chan struct{})}
		if request.uploadBudgetOnly {
			entry.meta.Reason = api.NetworkHoldUploadBudget
		}
		if !api.ValidPendingNetworkRequest(h.publicView(entry)) {
			return networkHoldTicket{}, changes, errHoldEnded
		}
		h.entries[key], h.byID[id] = entry, entry
		h.revision++
		entry.meta.Revision = h.revision
		changes = append(changes, networkHoldChange{Revision: h.revision})
	}
	if !waiting {
		return networkHoldTicket{entry: entry, changed: h.changed}, changes, nil
	}
	if entry.meta.Waiting >= api.NetworkHoldWaiterLimit {
		return networkHoldTicket{}, changes, errHoldEnded
	}
	h.next++
	entry.waiters[h.next] = request.budgetPermit
	entry.meta.Waiting++
	h.revision++
	entry.meta.Revision = h.revision
	changes = append(changes, networkHoldChange{Revision: h.revision})
	return networkHoldTicket{entry: entry, waiter: h.next, changed: h.changed}, changes, nil
}

func (h *networkHolds) detach(ticket networkHoldTicket) []networkHoldChange {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := ticket.entry
	if entry == nil || h.byID[entry.meta.ID] != entry {
		return nil
	}
	if _, exists := entry.waiters[ticket.waiter]; !exists {
		return nil
	}
	delete(entry.waiters, ticket.waiter)
	entry.meta.Waiting--
	h.revision++
	entry.meta.Revision = h.revision
	return []networkHoldChange{{Revision: h.revision}}
}

func (h *networkHolds) finishLocked(entry *networkHold, reason string, now time.Time) networkHoldChange {
	delete(h.entries, networkHoldKey{host: entry.request.Host, port: entry.request.Port, uploadBudget: entry.request.uploadBudgetOnly})
	delete(h.byID, entry.meta.ID)
	entry.meta.Status, entry.meta.Waiting = api.NetworkHoldClosed, 0
	h.revision++
	entry.meta.Revision = h.revision
	entry.reason = reason
	clear(entry.waiters)
	entry.request = Request{}
	entry.originalHost = ""
	close(entry.done)
	if reason == "refused" || reason == "expired" {
		h.refusals = append(h.refusals, now)
		// Only the newest five timestamps affect the five-in-ten-minute
		// stop. Retain that exact threshold without an unbounded history.
		slices.SortFunc(h.refusals, func(a, b time.Time) int { return a.Compare(b) })
		if len(h.refusals) > 5 {
			copy(h.refusals, h.refusals[len(h.refusals)-5:])
			h.refusals = h.refusals[:5]
		}
	}
	return networkHoldChange{Revision: h.revision, Reason: reason}
}

func (h *networkHolds) expireLocked(now time.Time) []networkHoldChange {
	var changes []networkHoldChange
	for _, entry := range h.entries {
		if !now.Before(entry.meta.Expires) {
			changes = append(changes, h.finishLocked(entry, "expired", now))
		}
	}
	h.trimRefusalsLocked(now)
	return changes
}

func (h *networkHolds) refuse(id string, now time.Time) (networkHoldChange, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.byID[id]
	if entry == nil || !now.Before(entry.meta.Expires) {
		return networkHoldChange{}, false
	}
	return h.finishLocked(entry, "refused", now), true
}

func (h *networkHolds) answer(ticket networkHoldTicket, now time.Time) (networkHoldChange, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := ticket.entry
	if entry == nil || h.byID[entry.meta.ID] != entry || !now.Before(entry.meta.Expires) {
		return networkHoldChange{}, false
	}
	return h.finishLocked(entry, "answered", now), true
}

func (h *networkHolds) snapshot() api.NetworkHoldSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := api.NetworkHoldSnapshot{Version: api.NetworkHoldV1, Revision: h.revision, Requests: make([]api.PendingNetworkRequest, 0, len(h.entries))}
	for _, entry := range h.entries {
		result.Requests = append(result.Requests, h.publicView(entry))
	}
	slices.SortFunc(result.Requests, func(a, b api.PendingNetworkRequest) int {
		if order := a.Opened.Compare(b.Opened); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result
}

func (h *networkHolds) policyChanged(reset bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if reset {
		h.refusals = nil
		clear(h.classifierRefusals)
	}
	if !h.closed {
		// Source-bound redaction may have changed even when no pending
		// target changed. Every next public snapshot reprojects its host.
		h.revision++
		for _, entry := range h.entries {
			entry.meta.Revision = h.revision
		}
		close(h.changed)
		h.changed = make(chan struct{})
	}
}

func (h *networkHolds) expire(now time.Time) []networkHoldChange {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.expireLocked(now)
}

func (h *networkHolds) nextExpiry() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	var next time.Time
	for _, entry := range h.entries {
		if next.IsZero() || entry.meta.Expires.Before(next) {
			next = entry.meta.Expires
		}
	}
	for _, at := range h.refusals {
		if due := at.Add(10 * time.Minute); next.IsZero() || due.Before(next) {
			next = due
		}
	}
	for _, at := range h.classifierRefusals {
		if due := at.Add(10 * time.Minute); next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next
}

func (h *networkHolds) currentRevision() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.revision
}

func (h *networkHolds) refusedCount(now time.Time) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trimRefusalsLocked(now)
	return len(h.refusals) + len(h.classifierRefusals)
}

func (h *networkHolds) trimRefusalsLocked(now time.Time) {
	before := len(h.refusals) + len(h.classifierRefusals)
	h.refusals = slices.DeleteFunc(h.refusals, func(at time.Time) bool { return !now.Before(at.Add(10 * time.Minute)) })
	for key, at := range h.classifierRefusals {
		if !now.Before(at.Add(10 * time.Minute)) {
			delete(h.classifierRefusals, key)
		}
	}
	if before != len(h.refusals)+len(h.classifierRefusals) {
		h.revision++
	}
}

func (h *networkHolds) noteClassifierRefusal(request Request, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := networkHoldKey{host: request.Host, port: request.Port, uploadBudget: request.uploadBudgetOnly}
	if _, counted := h.classifierRefusals[key]; !counted && len(h.classifierRefusals) < 5 {
		h.classifierRefusals[key] = now
		h.revision++
	}
}

func (h *networkHolds) notice(ticket networkHoldTicket) (<-chan struct{}, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ticket.entry == nil || h.byID[ticket.entry.meta.ID] != ticket.entry {
		if ticket.entry != nil {
			return nil, ticket.entry.reason
		}
		return nil, "ended"
	}
	return h.changed, ""
}

// lookup remains inside the trusted backend. Public callers receive only
// opaque descriptors; masked text is never parsed into this true target.
func (h *networkHolds) lookup(id string, now time.Time) (Request, uint64, time.Time, bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked(now)
	entry := h.byID[id]
	if entry == nil {
		return Request{}, 0, time.Time{}, false, false
	}
	display := h.publicView(entry)
	return entry.request, entry.meta.Revision, entry.meta.Expires, display.Redacted, true
}

func (h *networkHolds) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.changed)
	for _, entry := range h.entries {
		h.finishLocked(entry, "ended", time.Time{})
	}
}
