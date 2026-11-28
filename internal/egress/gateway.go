package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
)

// GatewayPathPrefix is the URL prefix served by the egress model gateway on
// the sandbox's plain-HTTP proxy endpoint (e.g. http://127.0.0.1:3128/gw/anthropic).
const GatewayPathPrefix = "/gw/"

// GatewayRouteConfig describes one model gateway route served by the egress
// process (decision 0053, spec 7.2 and 7.3).
type GatewayRouteConfig struct {
	Route    string             `json:"route"`           // e.g. "anthropic", "openai"
	Upstream string             `json:"upstream"`        // e.g. "https://api.anthropic.com"
	Guard    *agents.ModelGuard `json:"guard,omitempty"` // nil disables model body/path guard for custom routes
}

type gatewayRoute struct {
	slug         string
	upstream     *url.URL
	upstreamHost string
	upstreamPort int
	guard        *agents.ModelGuard
}

// GatewayOptions configures a model Gateway backed by httputil.ReverseProxy.
type GatewayOptions struct {
	Engine    *Engine
	Dialer    *SafeDialer
	Secrets   *SecretStore
	Usage     *UsageTracker
	Redactor  *Redactor
	Audit     AuditSink
	Routes    []GatewayRouteConfig
	TLSConfig *tls.Config // optional test override; production uses verified system roots
}

// Gateway terminates plain-HTTP model API calls from inside the sandbox, strips
// every guest credential header, enforces ModelGuard, injects the real key from
// SecretStore, forwards over verified upstream TLS via httputil.ReverseProxy
// without TLS interception, and counts token usage.
type Gateway struct {
	engine   *Engine
	dialer   *SafeDialer
	secrets  *SecretStore
	usage    *UsageTracker
	redactor *Redactor
	audit    AuditSink
	routes   map[string]*gatewayRoute
	proxy    *httputil.ReverseProxy
}

type gatewayStateKey struct{}

type gatewayReqState struct {
	route        *gatewayRoute
	upstreamPath string
	redactedPath string
	credName     string
	credHeader   string
	credValue    string
	decRule      string
	reqBytes     int64
	method       string
	audited      atomic.Bool
}

// NewGateway constructs a Gateway from opts and registers every route's
// upstream host on opts.Engine so direct CONNECT to model hosts is refused.
func NewGateway(opts GatewayOptions) (*Gateway, error) {
	if opts.Engine == nil {
		return nil, errors.New("egress gateway: Engine is required. Try: construct an Engine with NewEngine")
	}
	if opts.Dialer == nil {
		return nil, errors.New("egress gateway: Dialer is required. Try: construct a SafeDialer")
	}
	secrets := opts.Secrets
	if secrets == nil {
		secrets = NewSecretStore()
	}
	usage := opts.Usage
	if usage == nil {
		usage = NewUsageTracker()
	}
	redactor := opts.Redactor
	if redactor == nil {
		redactor = NewRedactor(nil)
	}

	routes := make(map[string]*gatewayRoute, len(opts.Routes))
	hosts := make([]string, 0, len(opts.Routes))
	for _, rc := range opts.Routes {
		r, err := compileGatewayRoute(rc)
		if err != nil {
			return nil, err
		}
		routes[r.slug] = r
		hosts = append(hosts, r.upstreamHost)
		if r.guard != nil {
			hosts = append(hosts, r.guard.Hosts...)
		}
	}
	if err := opts.Engine.RegisterGatewayHosts(hosts); err != nil {
		return nil, err
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.TLSConfig != nil {
		tlsCfg = opts.TLSConfig.Clone()
		if tlsCfg.MinVersion < tls.VersionTLS12 {
			tlsCfg.MinVersion = tls.VersionTLS12
		}
		tlsCfg.InsecureSkipVerify = false
	}

	tr := &http.Transport{
		DialContext:           opts.Dialer.DialContext,
		Proxy:                 nil,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	g := &Gateway{
		engine:   opts.Engine,
		dialer:   opts.Dialer,
		secrets:  secrets,
		usage:    usage,
		redactor: redactor,
		audit:    opts.Audit,
		routes:   routes,
	}
	g.proxy = &httputil.ReverseProxy{
		Transport:      tr,
		FlushInterval:  -1, // flush SSE chunks immediately
		Rewrite:        g.rewriteRequest,
		ModifyResponse: g.modifyResponse,
		ErrorHandler:   g.handleProxyError,
	}
	return g, nil
}

// ServeHTTP handles relative-path requests sent to the egress proxy. Requests
// under /gw/<route>/... are checked, armed with the host's real secret and
// proxied over verified TLS; any other relative request is refused.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, GatewayPathPrefix) {
		http.Error(w, "egress proxy: direct non-proxy requests are not allowed", http.StatusForbidden)
		return
	}
	route, rawRest, escapedRest, ok := g.splitRoute(r)
	if !ok {
		http.Error(w, "egress gateway: unknown model gateway route. Try: check the agent's gateway base URL", http.StatusNotFound)
		return
	}
	state, bodyBytes, ok := g.prepareRequest(w, r, route, rawRest, escapedRest)
	if !ok {
		return
	}
	addrs, err := g.dialer.ResolveAndCheck(r.Context(), route.upstreamHost, route.upstreamPort)
	if err != nil {
		status, decision := errHTTPStatus(err)
		g.emitAudit(AuditEvent{
			Kind: "net", Host: route.upstreamHost, Port: route.upstreamPort,
			Mode: string(ModeGateway), Method: r.Method, Path: state.redactedPath,
			Status: status, Rule: "range", Secret: state.credName, Decision: decision, Why: err.Error(),
		})
		writeProviderError(w, routeWire(route), status, providerForbiddenType(routeWire(route)), err.Error())
		return
	}

	reqCtx := withCheckedAddrs(r.Context(), addrs)
	reqCtx = context.WithValue(reqCtx, gatewayStateKey{}, state)
	reqCopy := r.Clone(reqCtx)
	reqCopy.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	reqCopy.ContentLength = int64(len(bodyBytes))
	reqCopy.TransferEncoding = nil
	reqCopy.Header.Del("Content-Encoding")
	if len(bodyBytes) > 0 {
		reqCopy.Header.Set("Content-Length", strconv.Itoa(len(bodyBytes)))
	} else {
		reqCopy.Header.Del("Content-Length")
	}
	g.proxy.ServeHTTP(w, reqCopy)
}

func (g *Gateway) prepareRequest(
	w http.ResponseWriter,
	r *http.Request,
	route *gatewayRoute,
	rawRest, escapedRest string,
) (*gatewayReqState, []byte, bool) {
	wire := routeWire(route)
	if isWebSocketUpgrade(r) {
		msg := fmt.Sprintf("%s: WebSocket upgrades are not allowed on the model gateway for %s. Try: set supports_websockets = false in the provider config", brand.Name, route.upstreamHost)
		g.rejectRequest(w, r, route, g.redactor.Path(rawRest), http.StatusForbidden, "model_guard", "websocket upgrade refused on gateway", providerForbiddenType(wire), msg)
		return nil, nil, false
	}
	upstreamPath, err := cleanUpstreamPath(rawRest, escapedRest)
	if err != nil {
		msg := fmt.Sprintf("%s: invalid model API path at %s: %s. Try: send a canonical path", brand.Name, route.upstreamHost, err.Error())
		g.rejectRequest(w, r, route, g.redactor.Path(rawRest), http.StatusForbidden, "model_guard", err.Error(), providerForbiddenType(wire), msg)
		return nil, nil, false
	}
	redactedPath := g.redactor.Path(upstreamPath)
	dec := g.engine.DecideGateway(route.upstreamHost, route.upstreamPort)
	if dec.Action != ActionAllow {
		msg := fmt.Sprintf("%s: %s is denied by policy (%s). Try: remove the deny rule before calling the model API", brand.Name, route.upstreamHost, dec.Rule)
		g.rejectRequest(w, r, route, redactedPath, http.StatusForbidden, dec.Rule, dec.Why, providerForbiddenType(wire), msg)
		return nil, nil, false
	}
	toolAllowed := func(grant string) bool {
		return g.engine.ToolAllowed(route.upstreamHost, grant)
	}
	if ref := checkModelPath(route.guard, r.Method, upstreamPath, toolAllowed); ref != nil {
		g.rejectRequest(w, r, route, redactedPath, http.StatusForbidden, "model_guard", ref.why, providerForbiddenType(wire), ref.message(g.engine.Session(), route.upstreamHost))
		return nil, nil, false
	}
	bodyBytes, err := decodeRequestBody(r.Body, r.Header.Get("Content-Encoding"))
	if err != nil {
		msg := fmt.Sprintf("%s: cannot read model request body for %s: %s. Try: send an uncompressed or valid gzip/deflate JSON body under 64 MiB", brand.Name, route.upstreamHost, err.Error())
		g.rejectRequest(w, r, route, redactedPath, http.StatusBadRequest, "model_guard", err.Error(), "invalid_request_error", msg)
		return nil, nil, false
	}
	if shouldInspectBody(r.Method, upstreamPath, route.guard) {
		if ref := checkModelBody(route.guard, g.engine.Preset(), bodyBytes, toolAllowed); ref != nil {
			g.rejectRequest(w, r, route, redactedPath, http.StatusForbidden, "model_guard", ref.why, providerForbiddenType(wire), ref.message(g.engine.Session(), route.upstreamHost))
			return nil, nil, false
		}
	}
	credName, credHeader, credValue, armed := g.secrets.Lookup(route.slug)
	if !armed {
		msg := fmt.Sprintf("%s: no bound credential is armed for model gateway route %q (%s). Try: arm the session secret from the laptop", brand.Name, route.slug, route.upstreamHost)
		g.rejectRequest(w, r, route, redactedPath, http.StatusUnauthorized, "secret_unarmed", "bound secret not armed", providerAuthErrorType(wire), msg)
		return nil, nil, false
	}
	return &gatewayReqState{
		route:        route,
		upstreamPath: upstreamPath,
		redactedPath: redactedPath,
		credName:     credName,
		credHeader:   credHeader,
		credValue:    credValue,
		decRule:      dec.Rule,
		reqBytes:     int64(len(bodyBytes)),
		method:       r.Method,
	}, bodyBytes, true
}

func (g *Gateway) rejectRequest(w http.ResponseWriter, r *http.Request, route *gatewayRoute, redactedPath string, status int, rule, why, errType, msg string) {
	g.emitAudit(AuditEvent{
		Kind: "net", Host: route.upstreamHost, Port: route.upstreamPort,
		Mode: string(ModeGateway), Method: r.Method, Path: redactedPath,
		Status: status, Rule: rule, Decision: string(ActionDeny), Why: why,
	})
	writeProviderError(w, routeWire(route), status, errType, msg)
}

func (g *Gateway) rewriteRequest(pr *httputil.ProxyRequest) {
	state, ok := pr.In.Context().Value(gatewayStateKey{}).(*gatewayReqState)
	if !ok {
		return
	}
	out := pr.Out
	out.URL.Scheme = state.route.upstream.Scheme
	out.URL.Host = state.route.upstream.Host
	out.URL.Path = state.upstreamPath
	out.URL.RawPath = ""
	out.URL.RawQuery = pr.In.URL.RawQuery
	out.Host = state.route.upstream.Host

	// Drop every credential header the guest sent so a planted key inside the
	// sandbox never reaches the provider.
	stripGuestCredentials(out.Header, state.credHeader)
	out.Header.Set(state.credHeader, state.credValue)
	out.Header.Set("Accept-Encoding", "identity")
	out.Header.Del("Content-Encoding")
}

func (g *Gateway) modifyResponse(resp *http.Response) error {
	state, ok := resp.Request.Context().Value(gatewayStateKey{}).(*gatewayReqState)
	if !ok {
		return nil
	}
	wire := routeWire(state.route)
	var obs *usageObserver
	if shouldCountUsage(wire, state.method, state.upstreamPath, resp.StatusCode) {
		obs = newUsageObserver(wire, state.reqBytes, resp.Header.Get("Content-Type"))
	}
	onDone := func(down int64, u *TokenUsage) {
		if state.audited.Swap(true) {
			return
		}
		usageKind := ""
		if u != nil {
			g.usage.Add(*u)
			if u.Estimated {
				usageKind = "estimate"
			} else {
				usageKind = "reported"
			}
		}
		g.emitAudit(AuditEvent{
			Kind: "net", Host: state.route.upstreamHost, Port: state.route.upstreamPort,
			Mode: string(ModeGateway), Method: state.method, Path: state.redactedPath,
			Status: resp.StatusCode, Up: state.reqBytes, Down: down,
			Rule: state.decRule, Secret: state.credName, Decision: string(ActionAllow),
			Usage: u, UsageKind: usageKind,
		})
	}
	if resp.Body == nil {
		onDone(0, nil)
		return nil
	}
	wrapped, decompressed := newUsageReadCloser(resp.Body, resp.Header.Get("Content-Encoding"), obs, onDone)
	resp.Body = wrapped
	if decompressed {
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
	}
	return nil
}

func (g *Gateway) handleProxyError(w http.ResponseWriter, r *http.Request, err error) {
	state, ok := r.Context().Value(gatewayStateKey{}).(*gatewayReqState)
	if !ok {
		http.Error(w, "egress gateway error", http.StatusBadGateway)
		return
	}
	status, decision := errHTTPStatus(err)
	if !state.audited.Swap(true) {
		g.emitAudit(AuditEvent{
			Kind: "net", Host: state.route.upstreamHost, Port: state.route.upstreamPort,
			Mode: string(ModeGateway), Method: state.method, Path: state.redactedPath,
			Status: status, Up: state.reqBytes, Rule: state.decRule,
			Secret: state.credName, Decision: decision, Why: err.Error(),
		})
	}
	wire := routeWire(state.route)
	msg := fmt.Sprintf("%s: upstream request to %s failed: %s. Try: check host network connectivity and TLS certificates", brand.Name, state.route.upstreamHost, err.Error())
	writeProviderError(w, wire, status, "api_error", msg)
}

func (g *Gateway) splitRoute(r *http.Request) (route *gatewayRoute, rawRest, escapedRest string, ok bool) {
	trimmed := strings.TrimPrefix(r.URL.Path, GatewayPathPrefix)
	slug, rest, hasRest := strings.Cut(trimmed, "/")
	route = g.routes[normalizeRouteSlug(slug)]
	if route == nil {
		return nil, "", "", false
	}
	if !hasRest {
		rawRest = "/"
	} else {
		rawRest = "/" + rest
	}
	escTrimmed := strings.TrimPrefix(r.URL.EscapedPath(), GatewayPathPrefix)
	_, escRest, escHas := strings.Cut(escTrimmed, "/")
	if !escHas {
		escapedRest = "/"
	} else {
		escapedRest = "/" + escRest
	}
	return route, rawRest, escapedRest, true
}

func (g *Gateway) emitAudit(ev AuditEvent) {
	if g.audit == nil {
		return
	}
	ev.V = 1
	if ev.T.IsZero() {
		ev.T = time.Now().UTC()
	}
	g.audit(ev)
}
