package egress

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxAuditPathBytes = 256

// AuditEvent is one JSON audit line emitted by the egress process for each
// flow, HTTP request or DNS query (spec 7.2). It never carries query strings,
// header values or bodies.
type AuditEvent struct {
	V           int         `json:"v"`
	T           time.Time   `json:"t"`
	Kind        string      `json:"kind"` // "net" or "dns"
	Host        string      `json:"host"`
	Port        int         `json:"port,omitempty"`
	Mode        string      `json:"mode,omitempty"` // "splice", "http", "gateway", "dns"
	Method      string      `json:"method,omitempty"`
	Path        string      `json:"path,omitempty"`
	Status      int         `json:"status,omitempty"`
	Up          int64       `json:"up,omitempty"`
	Down        int64       `json:"down,omitempty"`
	Rule        string      `json:"rule,omitempty"`
	Secret      string      `json:"secret,omitempty"`
	Decision    string      `json:"decision"` // "allow", "allow-unknown", "deny", "error"
	Why         string      `json:"why,omitempty"`
	Usage       *TokenUsage `json:"tokens,omitempty"`
	UsageKind   string      `json:"usage,omitempty"` // "reported" or "estimate"
	BudgetUsed  int64       `json:"budget_used,omitempty"`
	BudgetLimit int64       `json:"budget_limit,omitempty"`
}

// AuditSink receives audit events produced by the egress server.
type AuditSink func(AuditEvent)

// Redactor replaces exposed secret values in audit paths with [NAME] and
// strips query strings, truncating cleanly to 256 bytes.
type Redactor struct {
	mu    sync.RWMutex
	pairs []secretPair
}

type secretPair struct {
	name  string
	value string
}

// NewRedactor builds a Redactor from a map of secret name to secret value.
func NewRedactor(secrets map[string]string) *Redactor {
	r := &Redactor{}
	r.Set(secrets)
	return r
}

// Set replaces the redactor's secret map atomically. Longer values are matched
// first so a shorter secret never splits a longer one.
func (r *Redactor) Set(secrets map[string]string) {
	pairs := make([]secretPair, 0, len(secrets))
	for name, val := range secrets {
		if name == "" || val == "" {
			continue
		}
		pairs = append(pairs, secretPair{name: name, value: val})
	}
	sortSecretPairs(pairs)
	r.mu.Lock()
	r.pairs = pairs
	r.mu.Unlock()
}

// Merge adds or updates secret values in the redactor without removing
// existing entries.
func (r *Redactor) Merge(secrets map[string]string) {
	if r == nil || len(secrets) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	merged := make(map[string]string, len(r.pairs)+len(secrets))
	for _, p := range r.pairs {
		merged[p.name] = p.value
	}
	for name, val := range secrets {
		if name == "" || val == "" {
			continue
		}
		merged[name] = val
	}
	pairs := make([]secretPair, 0, len(merged))
	for name, val := range merged {
		pairs = append(pairs, secretPair{name: name, value: val})
	}
	sortSecretPairs(pairs)
	r.pairs = pairs
}

func sortSecretPairs(pairs []secretPair) {
	slices.SortFunc(pairs, func(a, b secretPair) int {
		if c := cmp.Compare(len(b.value), len(a.value)); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
}

// Path strips any query string or fragment, replaces every known secret value
// with [NAME], and cuts the result to at most 256 valid UTF-8 bytes.
func (r *Redactor) Path(raw string) string {
	p := raw
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if r != nil {
		r.mu.RLock()
		pairs := r.pairs
		r.mu.RUnlock()
		for _, sp := range pairs {
			if strings.Contains(p, sp.value) {
				p = strings.ReplaceAll(p, sp.value, "["+sp.name+"]")
			}
		}
	}
	return truncateUTF8(p, maxAuditPathBytes)
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
