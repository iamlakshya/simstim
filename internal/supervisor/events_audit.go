package supervisor

import (
	"encoding/json"
	"io"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// writeEvent uses the same original append resource as every native audit line.
// Only the prepared redacted V1 event enters it; no raw guest state is parsed.
func (a *auditLog) writeEvent(event alerts.Event) error {
	failure := func() error {
		return &proto.Error{Code: "events_unavailable", Message: "the native event could not be persisted", Hint: "Check the session's audit log and free disk space before reading events again."}
	}
	if a == nil {
		return failure()
	}
	if event.V != alerts.Version || !alerts.IsStreamKind(event.Event) || event.Detail != "" || event.DetailFrom != "" || event.Stream == "" || event.Sequence == 0 {
		return failure()
	}
	body, err := json.Marshal(auditLine{Time: time.Now().UTC(), Event: "session_event", Fields: map[string]any{"value": event}})
	if err != nil || len(body)+1 > host.EventAuditLineMax {
		return failure()
	}
	line := append(body, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return failure()
	}
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(a.f.Name()), false)
	if err != nil {
		return failure()
	}
	defer root.Close()
	reference, err := cachefs.CaptureFile(root, filepath.Base(a.f.Name()), a.f, 0o600)
	if err != nil {
		return failure()
	}
	n, err := a.f.Write(line)
	if err != nil || n != len(line) {
		if err == nil {
			err = io.ErrShortWrite
		}
		if a.log != nil {
			a.log.Warn("session event was not persisted", "error", err)
		}
		return failure()
	}
	if err := reference.CheckCreatedFile(root, filepath.Base(a.f.Name()), a.f, 0o600); err != nil {
		return failure()
	}
	return nil
}
