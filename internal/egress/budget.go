package egress

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// UsageTracker accumulates model token usage safely across concurrent gateway
// requests.
type UsageTracker struct {
	mu      sync.Mutex
	total   TokenUsage
	byModel map[string]TokenUsage
}

// NewUsageTracker creates an empty UsageTracker.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{byModel: map[string]TokenUsage{}}
}

// Seed initializes the tracker from persisted session usage (for example on
// resume or restore) so cumulative totals and budgets survive restarts.
func (u *UsageTracker) Seed(summary UsageSummary) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.total = summary.Total
	u.byModel = make(map[string]TokenUsage, len(summary.ByModel))
	maps.Copy(u.byModel, summary.ByModel)
}

// Add records one completed model request's usage into the session totals.
func (u *UsageTracker) Add(rec TokenUsage) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	api.AddUsage(&u.total, rec)
	model := rec.Model
	if model == "" {
		model = "unknown"
	}
	cur := u.byModel[model]
	cur.Model = model
	api.AddUsage(&cur, rec)
	u.byModel[model] = cur
}

// TotalTokens returns the cumulative token count recorded so far.
func (u *UsageTracker) TotalTokens() int64 {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.total.TotalTokens
}

// Snapshot returns a copy of the session's cumulative usage totals.
func (u *UsageTracker) Snapshot() UsageSummary {
	return u.SnapshotWithBudget(0)
}

// SnapshotWithBudget returns a copy of the session's cumulative usage totals
// annotated with the active model token budget (0 for no limit).
func (u *UsageTracker) SnapshotWithBudget(budget int64) UsageSummary {
	if u == nil {
		return UsageSummary{Budget: budget}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	byModel := make(map[string]TokenUsage, len(u.byModel))
	maps.Copy(byModel, u.byModel)
	return UsageSummary{Total: u.total, ByModel: byModel, Budget: budget}
}

const uploadBudgetWindow = time.Hour

// errUploadBudgetExceeded signals that a host hit its hourly upload cap.
var errUploadBudgetExceeded = errors.New("upload budget exceeded")

// modelBudgetMessage formats the spec 17.8 refusal line with a Try: hint.
func modelBudgetMessage(session string, used, budget int64) string {
	who := session
	if who == "" {
		who = "session"
	}
	return fmt.Sprintf(
		"%s: %s used %d of its %d model tokens. Try: raise network.model_budget with %s policy edit, then %s policy apply",
		brand.Name, who, used, budget, brand.Name, brand.Name,
	)
}

// writeModelBudgetError writes a non-retriable HTTP 429 refusal matching the
// provider's error schema (spec 17.8, build facts 2.2):
//   - Anthropic: 429 billing_error with x-should-retry: false and no retry-after.
//   - OpenAI: 429 insufficient_quota (both type and code) with x-should-retry: false and no retry-after.
func writeModelBudgetError(w http.ResponseWriter, wire, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-should-retry", "false")
	w.Header().Del("Retry-After")
	w.WriteHeader(http.StatusTooManyRequests)

	if wire == WireAnthropicMessages {
		payload := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "billing_error",
				"message": message,
			},
			"request_id": "req_" + randomHex(12),
		}
		_ = json.NewEncoder(w).Encode(payload)
		return
	}
	payload := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "insufficient_quota",
			"param":   nil,
			"code":    "insufficient_quota",
		},
	}
	_ = json.NewEncoder(w).Encode(payload)
}

type uploadSample struct {
	at int64 // unix seconds
	n  int64
}

// UploadBudget enforces a rolling per-host hourly byte cap for uploads to
// non-model hosts (isolation spec 7.2, plan P2.7). A limit of 0 disables the cap.
type UploadBudget struct {
	mu    sync.Mutex
	limit int64
	now   func() time.Time
	hosts map[string][]uploadSample
}

// NewUploadBudget creates a per-host hourly upload budget capped at limit bytes
// (0 disables the cap).
func NewUploadBudget(limit int64) *UploadBudget {
	return NewUploadBudgetWithClock(limit, time.Now)
}

// NewUploadBudgetWithClock creates an UploadBudget with a custom clock for tests.
func NewUploadBudgetWithClock(limit int64, now func() time.Time) *UploadBudget {
	if now == nil {
		now = time.Now
	}
	return &UploadBudget{
		limit: max(limit, 0),
		now:   now,
		hosts: map[string][]uploadSample{},
	}
}

// SetLimit updates the per-host hourly byte limit live (0 disables the cap).
func (b *UploadBudget) SetLimit(limit int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.limit = max(limit, 0)
	b.mu.Unlock()
}

// Limit returns the current per-host hourly byte limit (0 means no limit).
func (b *UploadBudget) Limit() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit
}

// Remaining reports how many upload bytes remain for host in the current rolling
// hour, along with bytes already used and the active limit.
func (b *UploadBudget) Remaining(host string) (remaining, used, limit int64, limited bool) {
	if b == nil {
		return 0, 0, 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return 0, 0, 0, false
	}
	used = b.usedLocked(host, b.now())
	rem := max(b.limit-used, 0)
	return rem, used, b.limit, true
}

// Consume attempts to reserve n upload bytes for host in the current rolling hour.
// It returns how many bytes are permitted (up to n) and whether the cap was reached.
func (b *UploadBudget) Consume(host string, n int64) (allowed int64, exceeded bool, used, limit int64) {
	if b == nil || n <= 0 {
		return n, false, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		return n, false, 0, 0
	}
	now := b.now()
	used = b.usedLocked(host, now)
	rem := b.limit - used
	if rem <= 0 {
		return 0, true, used, b.limit
	}
	if n > rem {
		b.recordLocked(host, now, rem)
		return rem, true, used + rem, b.limit
	}
	b.recordLocked(host, now, n)
	return n, false, used + n, b.limit
}

func (b *UploadBudget) usedLocked(host string, now time.Time) int64 {
	cutoff := now.Add(-uploadBudgetWindow).Unix()
	samples := b.hosts[host]
	if len(samples) == 0 {
		return 0
	}
	keep := 0
	var total int64
	for _, s := range samples {
		if s.at <= cutoff {
			continue
		}
		samples[keep] = s
		keep++
		total += s.n
	}
	if keep == 0 {
		delete(b.hosts, host)
		return 0
	}
	b.hosts[host] = samples[:keep]
	return total
}

func (b *UploadBudget) recordLocked(host string, now time.Time, n int64) {
	if n <= 0 {
		return
	}
	sec := now.Unix()
	samples := b.hosts[host]
	if last := len(samples) - 1; last >= 0 && samples[last].at == sec {
		samples[last].n += n
		b.hosts[host] = samples
		return
	}
	b.hosts[host] = append(samples, uploadSample{at: sec, n: n})
}

func uploadBudgetMessage(session, host string, used, limit int64) string {
	who := session
	if who == "" {
		who = "session"
	}
	return fmt.Sprintf(
		"%s: %s reached its upload budget (%s of %s in the last hour) for %s. Try: raise network.upload_budget with %s policy edit, then %s policy apply",
		brand.Name, who, formatBytesShort(used), formatBytesShort(limit), host, brand.Name, brand.Name,
	)
}

func formatBytesShort(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func (s *Server) checkUploadBudget(host string) (used, limit int64, ok bool) {
	if s.UploadBudget == nil {
		return 0, 0, true
	}
	rem, used, limit, limited := s.UploadBudget.Remaining(host)
	if !limited || rem > 0 {
		return used, limit, true
	}
	return used, limit, false
}

func (s *Server) auditUploadBudget(host string, port int, mode, method, path string, up, down, used, limit int64) {
	s.emitAudit(AuditEvent{
		Kind: "net", Host: host, Port: port, Mode: mode, Method: method, Path: path,
		Status: http.StatusForbidden, Up: up, Down: down,
		Rule: "upload_budget", Decision: string(ActionDeny),
		Why:         fmt.Sprintf("upload budget reached (%s of %s)", formatBytesShort(used), formatBytesShort(limit)),
		BudgetUsed:  used,
		BudgetLimit: limit,
	})
}

type uploadBudgetReader struct {
	rc       io.ReadCloser
	budget   *UploadBudget
	host     string
	uploaded int64
	exceeded bool
	used     int64
	limit    int64
}

func (r *uploadBudgetReader) Read(p []byte) (int, error) {
	if r.exceeded {
		return 0, errUploadBudgetExceeded
	}
	n, err := r.rc.Read(p)
	if n > 0 {
		allowed, hit, used, limit := r.budget.Consume(r.host, int64(n))
		r.uploaded += allowed
		r.used, r.limit = used, limit
		if hit {
			r.exceeded = true
			return int(allowed), errUploadBudgetExceeded
		}
	}
	return n, err //nolint:wrapcheck // preserve io.EOF and underlying transport errors
}

func (r *uploadBudgetReader) Close() error {
	return r.rc.Close() //nolint:wrapcheck // preserve underlying close error
}

// copyUpWithBudget copies bytes from client to upstream while enforcing host's
// rolling upload budget. When the cap is reached, only the permitted prefix is
// written and hit is returned as true.
func copyUpWithBudget(upstream, client net.Conn, host string, budget *UploadBudget) (up int64, hit bool) {
	buf := make([]byte, 32<<10)
	for {
		n, rerr := client.Read(buf)
		if n > 0 {
			allowed, exceeded, _, _ := budget.Consume(host, int64(n))
			if allowed > 0 {
				wn, werr := upstream.Write(buf[:allowed])
				up += int64(wn)
				if werr != nil {
					return up, exceeded
				}
			}
			if exceeded {
				return up, true
			}
		}
		if rerr != nil {
			return up, false
		}
	}
}
