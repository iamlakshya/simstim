package supervisor

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/egress"
)

// auditLog is a walled session's audit log, audit.jsonl in its folder:
// what the trusted side did and decided, one JSON object a line (spec 2).
// Only the supervisor writes it, and never with a secret's value or a
// string from the guest.
type auditLog struct {
	mu  sync.Mutex
	f   *os.File
	log *slog.Logger
}

// auditLine is one line of the audit log.
type auditLine struct {
	Time   time.Time      `json:"time"`
	Event  string         `json:"event"`
	Fields map[string]any `json:"fields,omitempty"`
}

// openAudit opens the audit log at path for appending, mode 0600. When it
// cannot, the supervisor runs on and its log says why: no line is worth
// stopping a session for.
func openAudit(path string, log *slog.Logger) *auditLog {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Warn("no audit log", "err", err)
		return &auditLog{log: log}
	}
	return &auditLog{f: f, log: log}
}

// write appends one line: the event and its fields, which the trusted
// side sets.
func (a *auditLog) write(event string, fields map[string]any) {
	if a == nil {
		return
	}
	b, err := json.Marshal(auditLine{Time: time.Now().UTC(), Event: event, Fields: fields})
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return
	}
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		a.log.Warn("audit line not written", "event", event, "err", err)
	}
}

// close closes the audit log.
func (a *auditLog) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f != nil {
		a.f.Close()
		a.f = nil
	}
}

// writeNet appends one pre-scrubbed net audit record from the egress process.
func (a *auditLog) writeNet(e egress.AuditEntry) {
	if a == nil {
		return
	}
	e.V = 1
	e.Kind = "net"
	b, err := json.Marshal(e) //nolint:gosec // G117: Secret holds the secret's name, never its value (spec 7.2)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return
	}
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		a.log.Warn("net audit line not written", "err", err)
	}
}
