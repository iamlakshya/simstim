package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// maxAuditLineBytes bounds memory while accepting larger non-network records.
const maxAuditLineBytes = 1 << 20

// ReadAuditNetEntries reads net audit lines from audit.jsonl with optional filters.
func ReadAuditNetEntries(path string, a api.PolicyLogArgs) ([]egress.AuditEntry, error) {
	cutoff, err := auditCutoff(a.Since, time.Now())
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []egress.AuditEntry{}, nil
		}
		return nil, auditReadError()
	}
	defer f.Close()
	return readAuditNetEntries(f, a, cutoff)
}

func auditCutoff(since string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(since)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, UserError("bad_request", "invalid policy log start time", "Pass an RFC 3339 timestamp or a duration such as 10m.")
}

func readAuditNetEntries(r io.Reader, a api.PolicyLogArgs, cutoff time.Time) ([]egress.AuditEntry, error) {
	byFilter := strings.TrimSpace(a.By)
	out := []egress.AuditEntry{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 8<<10), maxAuditLineBytes)
	for sc.Scan() {
		var e egress.AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Kind != "net" {
			continue
		}
		if !auditEntryMatches(e, cutoff, byFilter) {
			continue
		}
		out = append(out, e)
	}
	if sc.Err() != nil {
		return nil, auditReadError()
	}
	if a.Max > 0 && len(out) > a.Max {
		out = out[len(out)-a.Max:]
	}
	return out, nil
}

func auditEntryMatches(entry api.NetAuditEntry, cutoff time.Time, by string) bool {
	if !cutoff.IsZero() {
		at, err := time.Parse(time.RFC3339, entry.T)
		if err != nil || at.Before(cutoff) {
			return false
		}
	}
	return by == "" || entry.By == by || entry.Decision == by || strings.Contains(entry.Rule, by)
}

func auditReadError() error {
	return UserError("audit_read", "cannot read the session's network audit", "Check that the session's audit log is readable and each line is under 1 MiB, then try again.")
}
