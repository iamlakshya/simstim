package egress

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/brand"
)

type inspectedAuditKey struct{}

func (s *Server) proxyHTTPRequest(w http.ResponseWriter, req *http.Request, hdr StreamHeader, scheme string, turn *http1Turn) (result *httpAuditResult) {
	result = new(httpAuditResult)
	select {
	case s.httpSem <- struct{}{}:
	case <-req.Context().Done():
		return
	}
	if turn != nil {
		// The proxy controls upload cancellation. Avoid the H1 server's
		// implicit read-before-response drain of an untrusted guest body.
		_ = http.NewResponseController(w).EnableFullDuplex()
	}
	originalBody := req.Body
	var bodyProgress *httpBodyProgress
	if req.Body != nil && req.Body != http.NoBody {
		bodyProgress = &httpBodyProgress{reader: req.Body, remaining: req.ContentLength}
		bodyProgress.closeRead = func() {
			if turn != nil {
				turn.stopRead()
			} else {
				_ = originalBody.Close()
			}
		}
		req.Body = bodyProgress
	}
	entry := AuditEntry{Host: hdr.Host, Port: hdr.Port, Mode: "intercept", Method: req.Method,
		Path: req.RequestURI, Status: http.StatusForbidden, Rule: "protocol", Decision: "deny"}
	refuseCancelled := func() {
		entry.Status, entry.Rule, entry.Decision = http.StatusForbidden, "http:cancelled", "deny"
		writeInspectedError(w, brand.Name+": HTTP request was cancelled. Try: check the current network policy before retrying.", entry.Status)
	}
	auditOriginal := originalRequestSecretView(req).Authority
	if turn != nil && turn.original != nil {
		auditOriginal = turn.original.Authority
	}
	var scope *originRequest
	var secrets *boundHTTPSecrets
	var model *modelRequest
	var usageWriter *modelUsageHTTPWriter
	var usageComplete atomic.Bool
	physical := physicalH2RequestDelivery(req)
	physicalRetained := false
	defer func() {
		if turn != nil && bodyProgress != nil && !bodyProgress.complete.Load() {
			turn.stopRead()
		}
		result.set(s.formatNetworkAudit(entry, auditOriginal))
		var finishOnce sync.Once
		finish := func(physicalCompletion *ModelUsageCompletionV1) {
			finishOnce.Do(func() {
				defer func() { <-s.httpSem }()
				if scope != nil {
					scope.mu.Lock()
					usageWS := scope.usageWS
					scope.mu.Unlock()
					if usageWS != nil {
						usageWS.close()
					}
				}
				complete := usageComplete.Load() && usageWriter != nil && !usageWriter.failed.Load() && scope != nil && scope.ctx.Err() == nil
				if turn != nil && turn.forward != nil {
					complete = complete && turn.forward.physicalComplete()
				}
				if scope != nil {
					scope.joinPhysicalUpload()
				}
				if model != nil && model.usage != nil {
					var completion ModelUsageCompletionV1
					if physicalCompletion != nil {
						completion = *physicalCompletion
					} else {
						completion = model.usage.FinishV1(complete)
					}
					applyUsageAudit(&entry, completion)
				}
				if scope != nil {
					scope.stopIO()
					if req.Body != nil && req.Body != http.NoBody {
						_ = req.Body.Close()
					}
					if scope.measured != nil {
						entry.Up, entry.Down = scope.measured.up.Load(), scope.measured.down.Load()
					}
				}
				if secrets != nil && secrets.misuse.Load() {
					entry.Status, entry.Rule, entry.Decision = http.StatusForbidden, "secret_misuse", "deny"
					if name := secrets.decoy.Load(); name != nil {
						entry.Rule, entry.Secret = "decoy", *name
					}
				}
				formatted := s.formatNetworkAudit(entry, auditOriginal)
				result.set(formatted)
				s.emitAudit(formatted, hdr.Host)
				if audited, ok := req.Context().Value(inspectedAuditKey{}).(*atomic.Bool); ok {
					audited.Store(true)
				}
				if scope != nil {
					scope.close()
				}
			})
		}
		if physical != nil {
			complete := usageComplete.Load() && usageWriter != nil && !usageWriter.failed.Load() && scope != nil && scope.ctx.Err() == nil
			var before func()
			if scope != nil {
				before = scope.joinPhysicalUpload
			}
			physical.finalizer(complete, before, func(completion ModelUsageCompletionV1) {
				if physicalRetained {
					finish(&completion)
				} else {
					finish(nil)
				}
			})
		} else if turn == nil {
			finish(nil)
		} else {
			turn.finishAudit = func() { finish(nil) }
			if entry.Status == http.StatusSwitchingProtocols {
				turn.settle()
			}
		}
	}()
	if turn != nil && turn.refusal != "" {
		entry.Method, entry.Path = turn.method, turn.target
		entry.Status = http.StatusBadRequest
		writeInspectedError(w, turn.refusal, entry.Status)
		return result
	}
	if err := validateInspectedFraming(req); err != nil {
		writeInspectedError(w, brand.Name+": "+err.Error(), http.StatusBadRequest)
		entry.Status = http.StatusBadRequest
		return
	}
	original := originalRequestSecretView(req)
	if turn != nil && turn.original != nil {
		original = *turn.original
	}
	request, err := canonicalHTTPRequest(req, hdr, scheme)
	request.via = hdr.Via
	if err != nil {
		writeInspectedError(w, brand.Name+": "+err.Error(), http.StatusBadRequest)
		entry.Status = http.StatusBadRequest
		return
	}
	if pending, ok := req.Context().Value(pendingNetworkRefusalKey{}).(pendingNetworkRefusal); ok {
		entry.Status, entry.Rule, entry.Decision = http.StatusForbidden, pending.decision.Rule, "deny"
		writeInspectedError(w, pending.line, http.StatusForbidden)
		return
	}
	eng := s.currentEngine()
	permit := eng.NewPermit()
	// Keep the lifetime independent of net/http's normal handler-return
	// cancellation. While the handler is active, propagate actual client
	// cancellation into its exact scope, including a blocked body decoder.
	ctx, cancel := context.WithCancel(context.WithoutCancel(req.Context()))
	tunnel, _ := req.Context().Value(httpTunnelKey{}).(*httpTunnel)
	scope = &originRequest{s: s, request: request, scheme: scheme, query: req.URL.RawQuery, permit: permit, cancel: cancel, ctx: ctx,
		tunnel: tunnel, body: originalBody, progress: bodyProgress, http2: req.ProtoMajor >= 2, physicalDelivery: physical}
	if err := scope.bindResponseWriter(w); err != nil {
		cancel()
		refuseCancelled()
		return
	}
	defer scope.releaseResponseWriter()
	stopClient := scope.watchClientCancellation(req.Context())
	defer stopClient()
	if failure := s.prepareDecoyHTTPPrefix(scope, req, original, scheme); failure != nil {
		entry.Status, entry.Rule, entry.Secret = failure.status, failure.auditRule(), failure.decoy
		writeInspectedError(w, brand.Name+": "+failure.detail.Message+". Try: "+failure.detail.Hint, entry.Status)
		return
	}
	plan, decision, failure := s.admitSecretHTTPRequest(scope, req, original, hdr, scheme)
	if failure == nil && !decision.Allow && (decision.unmatched || decision.classifiedRefusal) {
		var carrier net.Conn
		if scope.tunnel != nil {
			carrier = scope.tunnel.conn
		}
		waited := s.awaitUnknown(scope.ctx, scope.request, original.Authority, hdr.Via, carrier, func(engine *Engine, candidate Request) Decision {
			return engine.evaluateModelNetwork(candidate, scope.permit, evalPrecheck)
		})
		if waited.Allow {
			// Recheck the original raw fields, current source/pin and policy
			// together before any actual reservation after the wait.
			plan, decision, failure = s.admitSecretHTTPRequest(scope, req, original, hdr, scheme)
		} else {
			decision = waited
		}
	}
	if failure != nil {
		entry.Status, entry.Rule, entry.Secret = failure.status, failure.auditRule(), failure.decoy
		writeInspectedError(w, brand.Name+": "+failure.detail.Message+". Try: "+failure.detail.Hint, entry.Status)
		return
	}
	entry.Rule = decision.Rule
	entry.By = decision.By
	if !decision.Allow {
		message := s.networkRefusalLine(decision, scope.request, original.Authority)
		if decision.unknownPressure {
			message = unknownResourceLine()
		}
		if decision.httpRefusal == httpRefusalReadOnly {
			message = brand.Name + ": this service is read-only. Try: allow the exact write path and method."
		}
		if decision.httpRefusal == httpRefusalUnavailable {
			message = brand.Name + ": this service cannot be inspected. Try: review its splice-only policy."
		}
		writeInspectedError(w, message, http.StatusForbidden)
		return
	}
	if !scope.allowed(s.currentEngine(), s.dialer) {
		scope.abort()
		refuseCancelled()
		return
	}
	secrets, err = s.prepareBoundHTTPSecrets(scope, req, plan)
	if err != nil {
		if kind, _, _ := httpClass(scope.request.Host); kind == "model" {
			model = &modelRequest{request: scope.request, guard: s.currentEngine().modelGuard(scope.request)}
			entry.Status, entry.Rule = http.StatusForbidden, "model:protocol"
			writeModelRefusal(w, model, s.modelRefusalLine(model, "model WebSocket protocol"))
			return
		}
		entry.Status, entry.Rule = http.StatusBadRequest, "secret_protocol"
		writeInspectedError(w, brand.Name+": bound request protocol is unsupported. Try: send a complete uncompressed request with matching helpers.", entry.Status)
		return
	}
	{
		body, decoded, err := decodeHTTPBody(req.Body, req.Header)
		if err != nil {
			if ctx.Err() != nil {
				refuseCancelled()
				return
			}
			if kind, _, _ := httpClass(scope.request.Host); kind == "model" {
				model = &modelRequest{request: scope.request, guard: s.currentEngine().modelGuard(scope.request)}
				entry.Status, entry.Rule = http.StatusForbidden, "model:encoding"
				writeModelRefusal(w, model, s.modelRefusalLine(model, "complete model JSON"))
				return
			}
			entry.Status, entry.Rule = http.StatusUnsupportedMediaType, "http:encoding"
			writeInspectedError(w, brand.Name+": invalid or unsupported encoded request body. Try: send a complete identity, gzip or deflate body.", entry.Status)
			return
		}
		req.Body = body
		if decoded {
			req.Header.Del("Content-Encoding")
			req.Header.Del("Content-Length")
			req.ContentLength = -1
		}
		if secrets != nil && req.Body != nil && req.Body != http.NoBody {
			req.Body = &secretRequestTrailerBody{reader: req.Body, request: req, secrets: secrets}
		}
	}
	model, modelFailure := s.prepareModelHTTPRequest(scope, req)
	if modelFailure != "" {
		if model != nil && model.budgetRefusal != nil {
			entry.Status, entry.Rule = http.StatusTooManyRequests, "model:budget"
			s.emitModelBudget(scope.ctx, model.budgetRefusal)
			_ = model.budgetRefusal.WriteHTTPV1(w, cleanAuditText(s.sessionRef(), s.scrubber.snapshot(), 128))
			return
		}
		entry.Status, entry.Rule = http.StatusForbidden, "model:"+modelFailure
		writeModelRefusal(w, model, s.modelRefusalLine(model, modelFailure))
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return
	}
	if failure := s.preparePushHTTPRequest(scope, req); failure != "" {
		entry.Status, entry.Rule = http.StatusForbidden, "service:"+failure
		writeInspectedError(w, brand.Name+": this Git push is not allowed. Try: grant this repository and push only to the session branch prefix over HTTPS.", entry.Status)
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return
	}
	if failure := s.prepareServiceHTTPRequest(scope, req); failure != "" {
		entry.Status, entry.Rule = http.StatusForbidden, "service:"+failure
		writeInspectedError(w, brand.Name+": this service operation is not allowed. Try: send a supported complete JSON request and allow its exact write path and method for this session.", entry.Status)
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return
	}
	if req.Body != nil && req.Body != http.NoBody {
		delivery := &httpBodyProgress{reader: req.Body, remaining: req.ContentLength}
		delivery.closeRead = func() { _ = bodyProgress.Close() }
		if !scope.setDelivery(delivery) {
			refuseCancelled()
			return
		}
		req.Body = delivery
	}
	if model != nil && model.usage != nil {
		usageWriter = &modelUsageHTTPWriter{ResponseWriter: w, turn: model.usage}
		if physical != nil {
			if err := physical.retain(model.usage, nil); err != nil {
				entry.Status = http.StatusBadGateway
				writeInspectedError(w, errPhysicalH2Binding.Error(), entry.Status)
				return
			}
			physical.observation(false)
			physicalRetained = true
			usageWriter.physical = true
		} else if turn != nil && turn.forward != nil {
			usageWriter.physical = true
		}
		w = usageWriter
	}
	entry.Decision = "allow"
	ctx = context.WithValue(ctx, originRequestKey{}, scope)
	req = req.WithContext(ctx)
	proxy := &httputil.ReverseProxy{
		Transport: requestHTTPTransport{server: s, scope: scope},
		Rewrite: func(p *httputil.ProxyRequest) {
			// The URL and Host were canonicalised before policy. Do not add
			// X-Forwarded headers containing guest or host network details.
			p.Out.Host = req.Host
			// Body decoding populates the server request's trailer map at
			// EOF. Clone would retain only its initial nil values, so share
			// this one request-owned map until the transport finishes reading.
			p.Out.Trailer = req.Trailer
			p.Out.Header.Set("Accept-Encoding", "identity")
			p.Out.Header.Del("Proxy-Authorization")
			p.Out.Header.Del("Proxy-Connection")
			plan.applyHeaders(p.Out.Header)
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			if ctx.Err() != nil {
				refuseCancelled()
				return
			}
			entry.Status = http.StatusBadGateway
			if errors.Is(err, errSecretTrailerMisuse) || secrets != nil && secrets.misuse.Load() {
				entry.Status, entry.Decision, entry.Rule = http.StatusForbidden, "deny", "secret_misuse"
			}
			if errors.Is(err, ErrRefusedAddr) {
				entry.Status, entry.Decision, entry.Rule = http.StatusForbidden, "deny", "address:refused"
			}
			writeInspectedError(w, brand.Name+": origin request failed. Try: check the service and current network policy.", entry.Status)
		},
		ModifyResponse: func(resp *http.Response) error {
			if err := prepareHTTPResponse(resp, req.Method); err != nil {
				return err
			}
			if err := secrets.prepareResponse(req, resp, scope, model); err != nil {
				return err
			}
			entry.Status = resp.StatusCode
			if model != nil && model.usage != nil && resp.StatusCode != http.StatusSwitchingProtocols {
				configureModelUsageResponse(resp, model.usage, &usageComplete)
				usageWriter.active = true
				if physical != nil {
					physical.observation(true)
				}
				if turn != nil && turn.forward != nil {
					turn.forward.observeModelHTTP(model.usage, req.Method)
				}
			}
			if resp.StatusCode == http.StatusSwitchingProtocols {
				if model != nil && model.webSocket {
					scope.mu.Lock()
					usageWS := scope.usageWS
					scope.mu.Unlock()
					filtered, ok := resp.Body.(boundWebSocket)
					if usageWS == nil || !ok || turn == nil || turn.forward == nil {
						return errors.New("model upgrade has no actual forwarding observer")
					}
					usageWS.attach(filtered.state.fail)
					turn.forward.observeModelWebSocket(usageWS)
				}
				if turn != nil {
					turn.upgrade()
				}
				return nil
			}
			return nil
		},
	}
	proxy.ServeHTTP(w, req)
	return result
}
