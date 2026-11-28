package egress

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// BoundCredential configures one secret held in memory by the egress process
// for a model gateway route (decision 0032). The value never enters the sandbox
// and is never written to disk or logged.
type BoundCredential struct {
	Route  string     `json:"route"`            // gateway route slug, e.g. "anthropic" or "openai"
	Name   string     `json:"name"`             // secret name for audit logs, e.g. "ANTHROPIC_API_KEY"
	Header string     `json:"header"`           // outbound header name, e.g. "x-api-key" or "Authorization"
	Format string     `json:"format,omitempty"` // value template, e.g. "{value}" or "Bearer {value}"
	Value  api.Secret `json:"value"`            // redacted by fmt; kept in memory only
}

type storedCredential struct {
	name   string
	header string
	format string
	value  []byte
}

// SecretStore keeps bound credentials in memory only, indexed by gateway route
// slug. Values are wiped with zeros when replaced or when Close is called
// (decision 0032).
type SecretStore struct {
	mu    sync.RWMutex
	creds map[string]*storedCredential
}

// NewSecretStore creates an empty in-memory SecretStore.
func NewSecretStore() *SecretStore {
	return &SecretStore{creds: map[string]*storedCredential{}}
}

// Arm stores creds in memory for their routes, replacing any prior value for a
// route and zeroing replaced byte slices.
func (s *SecretStore) Arm(creds []BoundCredential) error {
	for _, c := range creds {
		route := normalizeRouteSlug(c.Route)
		if route == "" {
			return fmt.Errorf("egress: bound secret %q has an empty route. Try: name a gateway route such as anthropic or openai", c.Name)
		}
		if strings.TrimSpace(c.Header) == "" {
			return fmt.Errorf("egress: bound secret %q for route %q has no header. Try: set a header such as x-api-key or Authorization", c.Name, route)
		}
		if len(c.Value) == 0 {
			return fmt.Errorf("egress: bound secret %q for route %q has an empty value. Try: pass a non-empty secret value", c.Name, route)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range creds {
		route := normalizeRouteSlug(c.Route)
		if prev := s.creds[route]; prev != nil {
			clear(prev.value)
		}
		format := c.Format
		if format == "" {
			format = "{value}"
		}
		s.creds[route] = &storedCredential{
			name:   c.Name,
			header: http.CanonicalHeaderKey(strings.TrimSpace(c.Header)),
			format: format,
			value:  []byte(string(c.Value)),
		}
	}
	return nil
}

// Lookup returns the secret name, outbound header name and formatted header
// value for route.
func (s *SecretStore) Lookup(route string) (name, header, headerVal string, ok bool) {
	if s == nil {
		return "", "", "", false
	}
	key := normalizeRouteSlug(route)
	s.mu.RLock()
	sc := s.creds[key]
	if sc == nil || len(sc.value) == 0 {
		s.mu.RUnlock()
		return "", "", "", false
	}
	name = sc.name
	header = sc.header
	headerVal = strings.ReplaceAll(sc.format, "{value}", string(sc.value))
	s.mu.RUnlock()
	return name, header, headerVal, true
}

// RedactMap returns a map of secret names to raw values for registering with
// Redactor so audit paths never contain any bound secret value.
func (s *SecretStore) RedactMap() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.creds))
	for _, sc := range s.creds {
		if sc.name != "" && len(sc.value) > 0 {
			out[sc.name] = string(sc.value)
		}
	}
	return out
}

// Close zeros every stored credential slice in memory and clears the map.
func (s *SecretStore) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, sc := range s.creds {
		clear(sc.value)
		delete(s.creds, k)
	}
}

func normalizeRouteSlug(raw string) string {
	s := strings.Trim(strings.ToLower(strings.TrimSpace(raw)), "/")
	if s == "" || strings.ContainsAny(s, "/ \t\r\n") {
		return ""
	}
	return s
}
