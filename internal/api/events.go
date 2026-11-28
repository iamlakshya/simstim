package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FeatureEvents is the bridge hello feature advertising events.watch streaming
// (isolation spec 10.5, 17.6).
const FeatureEvents = "events"

// GitOpUnpulledCount is the host helper's git operation that counts commits
// ahead of a base commit in a repository inside the git scope.
const GitOpUnpulledCount = "repo.unpulled"

// Event kinds carried by `events.watch` and the `events` command (spec 17.6).
const (
	EventStateChanged    = "state_changed"
	EventNeedsInput      = "needs_input"
	EventTurnDone        = "turn_done"
	EventEnded           = "ended"
	EventEgressHeld      = "egress_held"
	EventClassifierAllow = "classifier_allow"
	EventSandboxReady    = "sandbox_ready"
	EventSandboxStopped  = "sandbox_stopped"
	EventSandboxError    = "sandbox_error"
)

// WatchEventKinds lists every event kind emitted by `events.watch` in spec order.
var WatchEventKinds = []string{
	EventStateChanged,
	EventNeedsInput,
	EventTurnDone,
	EventEnded,
	EventEgressHeld,
	EventClassifierAllow,
	EventSandboxReady,
	EventSandboxStopped,
	EventSandboxError,
}

// WatchEvent is one session or sandbox event streamed by `events.watch`
// (isolation spec 17.6, cli-spec 7.2).
type WatchEvent struct {
	V        int       `json:"v,omitempty"`
	Event    string    `json:"event"`
	Host     string    `json:"host,omitempty"`
	Session  string    `json:"session,omitempty"`
	Name     string    `json:"name,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Time     time.Time `json:"time"`
	State    string    `json:"state,omitempty"`
	Prev     string    `json:"prev,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	ExitCode *int      `json:"exit_code,omitempty"`
	Signal   string    `json:"signal,omitempty"`
	Target   string    `json:"target,omitempty"`
}

// EventsWatchArgs configures the `events.watch` streaming bridge op (spec 10.5).
type EventsWatchArgs struct {
	Session string `json:"session,omitempty"`
	Follow  bool   `json:"follow,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// PruneArgs asks a host to list or remove stopped sandboxes that have no
// unpulled commits (spec 17.6).
type PruneArgs struct {
	StoppedBefore *time.Time `json:"stopped_before,omitempty"`
	DryRun        bool       `json:"dry_run,omitempty"`
}

// PruneSession describes one stopped session evaluated by `prune`.
type PruneSession struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Agent           string    `json:"agent,omitempty"`
	State           string    `json:"state"`
	StoppedAt       time.Time `json:"stopped_at,omitzero"`
	UnpulledCommits int       `json:"unpulled_commits,omitempty"`
	Action          string    `json:"action"` // "removed", "would_remove", "kept", "error"
	Reason          string    `json:"reason,omitempty"`
	Error           string    `json:"error,omitempty"`
}

// PruneResult is the host's answer to `prune`.
type PruneResult struct {
	Sessions      []PruneSession `json:"sessions"`
	RemovedImages []string       `json:"removed_images,omitempty"`
}

// SessionFinishArgs asks the host to finish a throwaway (`new --rm`) session
// once its program has exited (spec 17.6).
type SessionFinishArgs struct {
	ID string `json:"id"`
}

// SessionFinishResult says whether a throwaway session was removed or stopped
// because commits were not pulled.
type SessionFinishResult struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Action          string `json:"action"` // "removed" or "stopped"
	UnpulledCommits int    `json:"unpulled_commits,omitempty"`
	Message         string `json:"message,omitempty"`
}

// ParseRunDuration parses a positive duration string such as "1m", "4h", "30s"
// or "7d" (supporting day suffixes alongside Go duration units). An empty
// string returns 0, nil.
func ParseRunDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") && !strings.ContainsAny(s[:len(s)-1], "hmsuµn") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil || n <= 0 || n > 3650 {
			return 0, fmt.Errorf("%q is not a positive duration such as 1m, 4h or 7d", raw)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a positive duration such as 1m, 4h or 7d", raw)
	}
	return d, nil
}
