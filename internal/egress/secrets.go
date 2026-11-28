package egress

import (
	"encoding/base64"
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
	Route  string       `json:"route"`            // gateway route slug, e.g. "anthropic", "openai" or "chatgpt"
	Name   string       `json:"name"`             // secret name for audit logs, e.g. "ANTHROPIC_API_KEY"
	Header string       `json:"header"`           // outbound header name, e.g. "x-api-key" or "Authorization"
	Format string       `json:"format,omitempty"` // value template, e.g. "{value}" or "Bearer {value}"
	Value  api.Secret   `json:"value"`            // redacted by fmt; kept in memory only
	OAuth  *OAuthConfig `json:"oauth,omitempty"`  // optional refreshable OAuth metadata (decision 0059)
}

type storedCredential struct {
	name          string
	header        string
	format        string
	value         []byte
	accountHeader string
	accountID     string
	oauthFormat   string
	tokenURL      string
	clientID      string
	refreshToken  []byte
	idToken       []byte
	expiresAt     int64
	authFile      string
	lockFile      string
	refreshMu     sync.Mutex
}

type resolvedCred struct {
	name         string
	header       string
	headerVal    string
	rawValue     string
	extraHeaders map[string]string
	hasRefresh   bool
	expiresAt    int64
}

type oauthRefreshSnap struct {
	name         string
	tokenURL     string
	clientID     string
	accountID    string
	refreshToken []byte
	idToken      []byte
	authFile     string
	lockFile     string
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
			clear(prev.refreshToken)
			clear(prev.idToken)
		}
		format := c.Format
		if format == "" {
			format = "{value}"
		}
		sc := &storedCredential{
			name:   c.Name,
			header: http.CanonicalHeaderKey(strings.TrimSpace(c.Header)),
			format: format,
			value:  []byte(string(c.Value)),
		}
		if c.OAuth != nil {
			sc.oauthFormat = c.OAuth.Format
			sc.tokenURL = strings.TrimSpace(c.OAuth.TokenURL)
			sc.clientID = strings.TrimSpace(c.OAuth.ClientID)
			if h := strings.TrimSpace(c.OAuth.AccountHeader); h != "" {
				sc.accountHeader = http.CanonicalHeaderKey(h)
			}
			sc.accountID = strings.TrimSpace(c.OAuth.AccountID)
			if len(c.OAuth.RefreshToken) > 0 {
				sc.refreshToken = []byte(string(c.OAuth.RefreshToken))
			}
			if len(c.OAuth.IDToken) > 0 {
				sc.idToken = []byte(string(c.OAuth.IDToken))
			}
			sc.expiresAt = c.OAuth.ExpiresAt
			sc.authFile = strings.TrimSpace(c.OAuth.AuthFile)
			sc.lockFile = strings.TrimSpace(c.OAuth.LockFile)
		}
		s.creds[route] = sc
	}
	return nil
}

// Lookup returns the secret name, outbound header name and formatted header
// value for route.
func (s *SecretStore) Lookup(route string) (name, header, headerVal string, ok bool) {
	rc, ok := s.lookupResolved(route)
	if !ok {
		return "", "", "", false
	}
	return rc.name, rc.header, rc.headerVal, true
}

func (s *SecretStore) lookupResolved(route string) (resolvedCred, bool) {
	if s == nil {
		return resolvedCred{}, false
	}
	key := normalizeRouteSlug(route)
	s.mu.RLock()
	sc := s.creds[key]
	if sc == nil || len(sc.value) == 0 {
		s.mu.RUnlock()
		return resolvedCred{}, false
	}
	rawVal := string(sc.value)
	rc := resolvedCred{
		name:       sc.name,
		header:     sc.header,
		headerVal:  formatCredentialHeader(sc.format, rawVal),
		rawValue:   rawVal,
		hasRefresh: len(sc.refreshToken) > 0 && sc.tokenURL != "" && sc.clientID != "",
		expiresAt:  sc.expiresAt,
	}
	if sc.accountHeader != "" && sc.accountID != "" {
		rc.extraHeaders = map[string]string{sc.accountHeader: sc.accountID}
	}
	s.mu.RUnlock()
	return rc, true
}

func (s *SecretStore) storedForRoute(route string) *storedCredential {
	if s == nil {
		return nil
	}
	key := normalizeRouteSlug(route)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds[key]
}

func (s *SecretStore) oauthSnapshot(route string) (oauthRefreshSnap, bool) {
	if s == nil {
		return oauthRefreshSnap{}, false
	}
	key := normalizeRouteSlug(route)
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc := s.creds[key]
	if sc == nil {
		return oauthRefreshSnap{}, false
	}
	return oauthRefreshSnap{
		name:         sc.name,
		tokenURL:     sc.tokenURL,
		clientID:     sc.clientID,
		accountID:    sc.accountID,
		refreshToken: append([]byte(nil), sc.refreshToken...),
		idToken:      append([]byte(nil), sc.idToken...),
		authFile:     sc.authFile,
		lockFile:     sc.lockFile,
	}, true
}

func (s *SecretStore) applyRefreshedOAuth(route string, toks OAuthTokens) resolvedCred {
	key := normalizeRouteSlug(route)
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.creds[key]
	if sc == nil {
		return resolvedCred{}
	}
	clear(sc.value)
	sc.value = []byte(string(toks.AccessToken))
	if toks.RefreshToken != "" {
		clear(sc.refreshToken)
		sc.refreshToken = []byte(string(toks.RefreshToken))
	}
	if toks.IDToken != "" {
		clear(sc.idToken)
		sc.idToken = []byte(string(toks.IDToken))
	}
	if toks.AccountID != "" {
		sc.accountID = toks.AccountID
	}
	if toks.ExpiresAt > 0 {
		sc.expiresAt = toks.ExpiresAt
	}
	rawVal := string(sc.value)
	rc := resolvedCred{
		name:       sc.name,
		header:     sc.header,
		headerVal:  formatCredentialHeader(sc.format, rawVal),
		rawValue:   rawVal,
		hasRefresh: len(sc.refreshToken) > 0 && sc.tokenURL != "" && sc.clientID != "",
		expiresAt:  sc.expiresAt,
	}
	if sc.accountHeader != "" && sc.accountID != "" {
		rc.extraHeaders = map[string]string{sc.accountHeader: sc.accountID}
	}
	return rc
}

func formatCredentialHeader(format, val string) string {
	if rest, ok := strings.CutPrefix(format, "basic:"); ok {
		raw := strings.ReplaceAll(rest, "{value}", val)
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
	}
	return strings.ReplaceAll(format, "{value}", val)
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
		if sc.name == "" {
			continue
		}
		if len(sc.value) > 0 {
			out[sc.name] = string(sc.value)
		}
		if len(sc.refreshToken) > 0 {
			out[sc.name+"_REFRESH"] = string(sc.refreshToken)
		}
		if len(sc.idToken) > 0 {
			out[sc.name+"_ID"] = string(sc.idToken)
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
		clear(sc.refreshToken)
		clear(sc.idToken)
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
