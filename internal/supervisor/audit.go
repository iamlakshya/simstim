package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

// AuditEntry is one JSON line in the session's trusted audit log
// (paths.AuditLog). It never carries secret values or terminal output.
type AuditEntry struct {
	Time     string `json:"time"`
	Event    string `json:"event"`
	Session  string `json:"session"`
	Wall     string `json:"wall,omitempty"`
	Driver   string `json:"driver,omitempty"`
	Detail   string `json:"detail,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Signal   string `json:"signal,omitempty"`
	OOM      bool   `json:"oom,omitempty"`
}

type auditLog struct {
	mu sync.Mutex
	f  *os.File
}

func openAuditLog(path string) (*auditLog, error) {
	if err := paths.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w. Try: check disk space and permissions in the state folder", err)
	}
	return &auditLog{f: f}, nil
}

func (a *auditLog) Record(e AuditEntry) {
	if a == nil {
		return
	}
	if e.Time == "" {
		e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	a.mu.Lock()
	if a.f != nil {
		_, _ = a.f.Write(b)
	}
	a.mu.Unlock()
}

func (a *auditLog) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	err := a.f.Close()
	a.f = nil
	if err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	return nil
}
