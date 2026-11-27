package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// EventAuditLineMax is the existing native audit reader's record bound.
const EventAuditLineMax = maxAuditLineBytes

// readEventAudit reads only exact native session_event records. Directories,
// life records and raw guest/net audit fields can never manufacture an event.
// The same bounded audit-line kernel, native file identity and no-follow root
// checks cover a fast failed supervisor after its socket has closed.
func readEventAudit(ctx context.Context, primary, target string) ([]alerts.Event, error) {
	bad := func() error {
		return UserError("events_unavailable", "the native session event audit is missing, changed or incomplete", "Check the session's audit log and supervisor log, then read events again.")
	}
	if !api.ValidSessionID(primary) || target != "" && !api.ValidSessionID(target) {
		return nil, bad()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(primary), false)
	if err != nil {
		return nil, bad()
	}
	defer root.Close()
	f, err := cachefs.OpenFile(root, "audit.jsonl", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, bad()
	}
	defer f.Close()
	reference, err := cachefs.CaptureFile(root, "audit.jsonl", f, 0o600)
	if err != nil {
		return nil, bad()
	}
	before, err := f.Stat()
	if err != nil {
		return nil, bad()
	}
	if _, err := auditPageFileID(before); err != nil {
		return nil, bad()
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(joined); _ = f.Close() })
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	start := max(int64(0), before.Size()-2*int64(maxAuditLineBytes))
	reader := bufio.NewReaderSize(io.NewSectionReader(f, start, before.Size()-start), 8<<10)
	if start > 0 {
		// A clipped first record is not interpreted as an event. Every complete
		// record after it remains subject to the same native audit-line bound.
		if _, err := readAuditPageLine(ctx, reader); err != nil {
			return nil, bad()
		}
	}
	out := make([]alerts.Event, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := reader.Peek(1); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, bad()
		}
		line, err := readAuditPageLine(ctx, reader)
		if err != nil {
			return nil, bad()
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(line, &envelope) != nil {
			return nil, bad()
		}
		var kind string
		if json.Unmarshal(envelope["event"], &kind) != nil || kind != "session_event" {
			continue
		}
		if len(envelope) != 3 {
			return nil, bad()
		}
		var at time.Time
		if json.Unmarshal(envelope["time"], &at) != nil || at.IsZero() {
			return nil, bad()
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(envelope["fields"], &fields) != nil || len(fields) != 1 || len(fields["value"]) == 0 {
			return nil, bad()
		}
		event, err := alerts.DecodeStreamEvent(fields["value"], "")
		if err != nil {
			return nil, bad()
		}
		if _, err := alerts.StreamEventIdentity(event); err != nil {
			return nil, bad()
		}
		if target != "" && event.Session.ID != target {
			continue
		}
		if len(out) == alerts.EventHistoryLimit {
			copy(out, out[1:])
			out = out[:len(out)-1]
		}
		out = append(out, event)
	}
	after, err := f.Stat()
	if err != nil || after.Size() < before.Size() {
		return nil, bad()
	}
	if err := reference.CheckCreatedFile(root, "audit.jsonl", f, 0o600); err != nil {
		return nil, bad()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
