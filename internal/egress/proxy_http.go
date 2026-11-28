package egress

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/elazarl/goproxy"
)

type httpAuditMeta struct {
	host     string
	port     int
	method   string
	path     string
	rule     string
	decision string
	upReader *uploadBudgetReader
}

func (s *Server) handleHTTPRequest(r *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		err := fmt.Errorf("egress: unsupported proxy scheme %q. Try: use http or https", r.URL.Scheme)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: r.URL.Host, Mode: string(ModeHTTP), Method: r.Method,
			Path: s.Redactor.Path(r.URL.Path), Status: http.StatusForbidden,
			Rule: "invalid", Decision: string(ActionDeny), Why: err.Error(),
		})
		return r, textResponse(r, http.StatusForbidden, err.Error()+"\n")
	}
	normHost, port, err := parseURLHostPort(r.URL.Host, r.URL.Scheme)
	if err != nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: r.URL.Host, Mode: string(ModeHTTP), Method: r.Method,
			Path: s.Redactor.Path(r.URL.Path), Status: http.StatusBadRequest,
			Rule: "invalid", Decision: string(ActionDeny), Why: err.Error(),
		})
		return r, textResponse(r, http.StatusBadRequest, err.Error()+"\n")
	}
	allowSlash := !s.Engine.HasPathRule(normHost, port)
	canon, err := CanonicalizeRequest(r, normHost, port, r.URL.Scheme, allowSlash)
	if err != nil {
		status, rule := http.StatusBadRequest, "smuggle"
		if errors.Is(err, ErrQueryTooLong) {
			status, rule = http.StatusRequestURITooLong, "query_too_long"
		} else if errors.Is(err, ErrHostMismatch) {
			rule = "host_mismatch"
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeHTTP), Method: r.Method,
			Path: s.Redactor.Path(r.URL.Path), Status: status,
			Rule: rule, Decision: string(ActionDeny), Why: err.Error(),
		})
		return r, textResponse(r, status, err.Error()+"\n")
	}
	u := *canon.URL
	r.URL = &u
	r.Host = u.Host

	redactedPath := s.Redactor.Path(canon.Path)
	dec := s.Engine.DecideHTTP(Request{
		Host: normHost, Port: port, Method: canon.Method, Path: canon.Path, Header: r.Header,
	})
	if dec.Action != ActionAllow {
		drainSmallBody(r)
		body := dec.Why
		if body == "" {
			body = s.denyBody(normHost, port, dec)
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeHTTP), Method: canon.Method,
			Path: redactedPath, Status: http.StatusForbidden, Rule: dec.Rule,
			Decision: string(ActionDeny), Why: dec.Why,
		})
		return r, textResponse(r, http.StatusForbidden, body+"\n")
	}

	if used, limit, ok := s.checkUploadBudget(normHost); !ok {
		body := uploadBudgetMessage(s.Engine.Session(), normHost, used, limit)
		s.auditUploadBudget(normHost, port, string(ModeHTTP), r.Method, redactedPath, 0, 0, used, limit)
		return r, textResponse(r, http.StatusForbidden, body+"\n")
	}

	addrs, err := s.Dialer.ResolveAndCheck(r.Context(), normHost, port)
	if err != nil {
		status, decision := errHTTPStatus(err)
		s.emitAudit(AuditEvent{
			Kind: "net", Host: normHost, Port: port, Mode: string(ModeHTTP), Method: r.Method,
			Path: redactedPath, Status: status, Rule: "range", Decision: decision, Why: err.Error(),
		})
		return r, textResponse(r, status, err.Error()+"\n")
	}

	var upReader *uploadBudgetReader
	if r.Body != nil && s.UploadBudget != nil && s.UploadBudget.Limit() > 0 {
		upReader = &uploadBudgetReader{rc: r.Body, budget: s.UploadBudget, host: normHost}
		r.Body = upReader
	}
	r = r.WithContext(withCheckedAddrs(r.Context(), addrs))
	ctx.UserData = httpAuditMeta{
		host: normHost, port: port, method: canon.Method, path: redactedPath,
		rule: dec.Rule, decision: auditDecision(dec), upReader: upReader,
	}
	return r, nil
}

func (s *Server) handleHTTPResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	meta, ok := ctx.UserData.(httpAuditMeta)
	if !ok {
		return resp
	}
	if resp == nil {
		if meta.upReader != nil && (meta.upReader.exceeded || errors.Is(ctx.Error, errUploadBudgetExceeded)) {
			body := uploadBudgetMessage(s.Engine.Session(), meta.host, meta.upReader.used, meta.upReader.limit)
			s.auditUploadBudget(meta.host, meta.port, string(ModeHTTP), meta.method, meta.path, meta.upReader.uploaded, 0, meta.upReader.used, meta.upReader.limit)
			return textResponse(ctx.Req, http.StatusForbidden, body+"\n")
		}
		why := "upstream error"
		if ctx.Error != nil {
			why = ctx.Error.Error()
		}
		status, decision := http.StatusBadGateway, "error"
		if ctx.Error != nil {
			status, decision = errHTTPStatus(ctx.Error)
		}
		s.emitAudit(AuditEvent{
			Kind: "net", Host: meta.host, Port: meta.port, Mode: string(ModeHTTP),
			Method: meta.method, Path: meta.path, Status: status,
			Rule: meta.rule, Decision: decision, Why: why,
		})
		return textResponse(ctx.Req, status, why+"\n")
	}
	var upBytes int64
	if meta.upReader != nil && meta.upReader.uploaded > 0 {
		upBytes = meta.upReader.uploaded
	} else if ctx.Req != nil && ctx.Req.ContentLength > 0 {
		upBytes = ctx.Req.ContentLength
	}
	if resp.Body == nil {
		s.emitAudit(AuditEvent{
			Kind: "net", Host: meta.host, Port: meta.port, Mode: string(ModeHTTP),
			Method: meta.method, Path: meta.path, Status: resp.StatusCode,
			Up: upBytes, Rule: meta.rule, Decision: meta.decision,
		})
		return resp
	}
	resp.Body = &auditReadCloser{
		ReadCloser: resp.Body,
		onDone: func(down int64) {
			s.emitAudit(AuditEvent{
				Kind: "net", Host: meta.host, Port: meta.port, Mode: string(ModeHTTP),
				Method: meta.method, Path: meta.path, Status: resp.StatusCode,
				Up: upBytes, Down: down, Rule: meta.rule, Decision: meta.decision,
			})
		},
	}
	return resp
}
