package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

type oauthRefreshPayload struct {
	ClientID      string `json:"client_id"`
	GrantType     string `json:"grant_type"`
	RefreshReason string `json:"refresh_reason,omitempty"`
	RefreshToken  string `json:"refresh_token"`
}

type oauthRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        any    `json:"error"`
}

func (g *Gateway) resolveCredential(ctx context.Context, route *gatewayRoute) (resolvedCred, bool, error) {
	rc, ok := g.secrets.lookupResolved(route.slug)
	if !ok {
		return resolvedCred{}, false, nil
	}
	if rc.hasRefresh && rc.expiresAt > 0 && time.Now().Add(oauthRefreshSkew).Unix() >= rc.expiresAt {
		refreshed, err := g.refreshOAuthRoute(ctx, route, rc.rawValue, "proactive_expiry")
		if err != nil {
			// If the current access token has not actually expired yet, let it try
			// upstream; if it is already expired, fail cleanly now.
			if time.Now().Unix() >= rc.expiresAt {
				return resolvedCred{}, true, err
			}
			return rc, true, nil
		}
		return refreshed, true, nil
	}
	return rc, true, nil
}

func (g *Gateway) refreshOAuthRoute(ctx context.Context, route *gatewayRoute, failedAccess, reason string) (resolvedCred, error) {
	sc := g.secrets.storedForRoute(route.slug)
	if sc == nil {
		return resolvedCred{}, errors.New("no OAuth credential armed for route")
	}
	sc.refreshMu.Lock()
	defer sc.refreshMu.Unlock()

	// Single-flight check: another concurrent request may have refreshed already.
	if cur, ok := g.secrets.lookupResolved(route.slug); ok && cur.rawValue != "" && cur.rawValue != failedAccess {
		return cur, nil
	}

	snap, ok := g.secrets.oauthSnapshot(route.slug)
	if !ok || len(snap.refreshToken) == 0 || snap.tokenURL == "" || snap.clientID == "" {
		return resolvedCred{}, errors.New("OAuth credential cannot be refreshed (missing refresh token or token endpoint)")
	}
	defer clear(snap.refreshToken)
	defer clear(snap.idToken)

	var updated resolvedCred
	err := withAuthFileLock(snap.lockFile, func() error {
		// Re-check the host's auth.json under flock: another session on this host
		// may already have rotated the single-use refresh token.
		if snap.authFile != "" {
			if fileToks, ferr := ReadCodexAuthFile(snap.authFile); ferr == nil {
				defer fileToks.Clear()
				adopted := string(fileToks.AccessToken)
				notExpired := fileToks.ExpiresAt == 0 || time.Now().Add(oauthRefreshSkew).Unix() < fileToks.ExpiresAt
				if adopted != "" && adopted != failedAccess && (reason == "unauthorized" || notExpired) {
					updated = g.secrets.applyRefreshedOAuth(route.slug, fileToks)
					g.redactor.Merge(g.secrets.RedactMap())
					g.emitAudit(AuditEvent{
						Kind: "net", Host: route.upstreamHost, Port: route.upstreamPort,
						Mode: string(ModeGateway), Method: http.MethodPost, Path: "/oauth/token",
						Status: http.StatusOK, Rule: "oauth_refresh", Secret: snap.name,
						Decision: string(ActionAllow), Why: "adopted refreshed token from host auth file",
					})
					return nil
				}
				// Even if access_token still needs refresh, prefer the latest refresh_token
				// on disk in case it rotated earlier.
				if fileToks.RefreshToken != "" {
					clear(snap.refreshToken)
					snap.refreshToken = []byte(string(fileToks.RefreshToken))
				}
				if fileToks.IDToken != "" {
					clear(snap.idToken)
					snap.idToken = []byte(string(fileToks.IDToken))
				}
			}
		}

		toks, tokenHost, tokenPort, rerr := g.callOAuthRefreshEndpoint(ctx, snap, reason)
		if rerr != nil {
			g.emitAudit(AuditEvent{
				Kind: "net", Host: tokenHost, Port: tokenPort,
				Mode: string(ModeGateway), Method: http.MethodPost, Path: "/oauth/token",
				Status: http.StatusUnauthorized, Rule: "oauth_refresh", Secret: snap.name,
				Decision: string(ActionDeny), Why: rerr.Error(),
			})
			return rerr
		}
		defer toks.Clear()

		updated = g.secrets.applyRefreshedOAuth(route.slug, toks)
		g.redactor.Merge(g.secrets.RedactMap())
		if snap.authFile != "" {
			// Best-effort atomic update of the host's pre-existing auth.json under flock.
			_ = writeCodexAuthFileAtomic(snap.authFile, toks)
		}
		g.emitAudit(AuditEvent{
			Kind: "net", Host: tokenHost, Port: tokenPort,
			Mode: string(ModeGateway), Method: http.MethodPost, Path: "/oauth/token",
			Status: http.StatusOK, Rule: "oauth_refresh", Secret: snap.name,
			Decision: string(ActionAllow), Why: "refreshed OAuth access token",
		})
		return nil
	})
	if err != nil {
		return resolvedCred{}, err
	}
	return updated, nil
}

func (g *Gateway) callOAuthRefreshEndpoint(ctx context.Context, snap oauthRefreshSnap, reason string) (OAuthTokens, string, int, error) {
	u, err := url.Parse(snap.tokenURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
		return OAuthTokens{}, "", 0, fmt.Errorf("invalid OAuth token URL %q", snap.tokenURL)
	}
	host, err := normalizeHost(u.Hostname())
	if err != nil {
		return OAuthTokens{}, u.Hostname(), 443, err
	}
	port := 443
	if p := u.Port(); p != "" {
		if n, perr := strconv.Atoi(p); perr == nil && n >= 1 && n <= 65535 {
			port = n
		}
	}
	if dec := g.engine.DecideGateway(host, port); dec.Action != ActionAllow {
		return OAuthTokens{}, host, port, fmt.Errorf("OAuth refresh host %s denied by policy (%s)", host, dec.Rule)
	}
	addrs, err := g.dialer.ResolveAndCheck(ctx, host, port)
	if err != nil {
		return OAuthTokens{}, host, port, fmt.Errorf("resolve OAuth refresh host %s: %w", host, err)
	}

	body, err := json.Marshal(oauthRefreshPayload{
		ClientID:      snap.clientID,
		GrantType:     "refresh_token",
		RefreshReason: reason,
		RefreshToken:  string(snap.refreshToken),
	})
	if err != nil {
		return OAuthTokens{}, host, port, fmt.Errorf("encode refresh request: %w", err)
	}
	defer clear(body)

	rctx, cancel := context.WithTimeout(withCheckedAddrs(ctx, addrs), oauthRefreshTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, snap.tokenURL, bytes.NewReader(body))
	if err != nil {
		return OAuthTokens{}, host, port, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := g.oauthClient.Do(req)
	if err != nil {
		return OAuthTokens{}, host, port, fmt.Errorf("OAuth token refresh failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthFileBytes))
	if err != nil {
		return OAuthTokens{}, host, port, fmt.Errorf("read OAuth refresh response: %w", err)
	}
	defer clear(respBytes)

	if resp.StatusCode != http.StatusOK {
		return OAuthTokens{}, host, port, fmt.Errorf("OAuth token refresh returned HTTP %d", resp.StatusCode)
	}
	var parsed oauthRefreshResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return OAuthTokens{}, host, port, fmt.Errorf("decode OAuth refresh response: %w", err)
	}
	acc := strings.TrimSpace(parsed.AccessToken)
	if acc == "" {
		return OAuthTokens{}, host, port, errors.New("OAuth refresh response had no access_token")
	}
	ref := strings.TrimSpace(parsed.RefreshToken)
	if ref == "" {
		ref = string(snap.refreshToken)
	}
	idt := strings.TrimSpace(parsed.IDToken)
	if idt == "" {
		idt = string(snap.idToken)
	}
	acctID := ParseJWTAccountID(idt)
	if acctID == "" {
		acctID = ParseJWTAccountID(acc)
	}
	if acctID == "" {
		acctID = snap.accountID
	}
	exp := ParseJWTExpiry(acc)
	if exp == 0 && parsed.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second).Unix()
	}
	return OAuthTokens{
		AccessToken:  api.Secret(acc),
		RefreshToken: api.Secret(ref),
		IDToken:      api.Secret(idt),
		AccountID:    acctID,
		ExpiresAt:    exp,
	}, host, port, nil
}

// refreshRoundTripper wraps the gateway's upstream http.Transport to perform one
// transparent OAuth refresh-and-retry when a subscription route answers 401.
type refreshRoundTripper struct {
	base *http.Transport
	gw   *Gateway
}

func (rt *refreshRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("gateway upstream round trip: %w", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	state, ok := req.Context().Value(gatewayStateKey{}).(*gatewayReqState)
	if !ok || !state.hasOAuthRefresh {
		return resp, nil
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAuthFileBytes))
	_ = resp.Body.Close()

	refreshed, rerr := rt.gw.refreshOAuthRoute(req.Context(), state.route, state.rawAccessToken, "unauthorized")
	if rerr != nil {
		return syntheticAuthExpiredResponse(req, routeWire(state.route), state.route.upstreamHost, rerr), nil
	}

	retryReq := req.Clone(req.Context())
	retryReq.Body = io.NopCloser(bytes.NewReader(state.bodyBytes))
	retryReq.ContentLength = int64(len(state.bodyBytes))
	stripGuestCredentials(retryReq.Header, refreshed.header)
	retryReq.Header.Set(refreshed.header, refreshed.headerVal)
	for k, v := range refreshed.extraHeaders {
		retryReq.Header.Set(k, v)
	}
	state.rawAccessToken = refreshed.rawValue
	retryResp, retryErr := rt.base.RoundTrip(retryReq)
	if retryErr != nil {
		return nil, fmt.Errorf("gateway upstream retry after OAuth refresh: %w", retryErr)
	}
	return retryResp, nil
}

func syntheticAuthExpiredResponse(req *http.Request, wire, host string, err error) *http.Response {
	msg := fmt.Sprintf(
		"%s: ChatGPT sign-in token for %s expired and refresh failed (%s). Try: run %s agents login codex <host> or update CODEX_AUTH_JSON in config.toml",
		brand.Name, host, err.Error(), brand.Name,
	)
	var body []byte
	if wire == WireAnthropicMessages {
		body, _ = json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "authentication_error", "message": msg},
		})
	} else {
		body, _ = json.Marshal(map[string]any{
			"error": map[string]any{
				"message": msg,
				"type":    "invalid_api_key",
				"param":   nil,
				"code":    "token_expired",
			},
		})
	}
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("x-should-retry", "false")
	return &http.Response{
		StatusCode:    http.StatusUnauthorized,
		Status:        "401 Unauthorized",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}
