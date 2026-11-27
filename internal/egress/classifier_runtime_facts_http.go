package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var errClassifierFact = errors.New("classifier fact lookup is unavailable. Try: check the optional RDAP setting and retry later")

func validFactURL(endpoint *url.URL) bool {
	if endpoint == nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Hostname() == "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.Port() != "" && endpoint.Port() != "443" {
		return false
	}
	host, err := NormaliseHost(endpoint.Hostname())
	return err == nil && host == endpoint.Hostname()
}

func (facts *classifierFacts) fetch(ctx context.Context, s *Server, target string, maximum int64) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || maximum <= 0 || maximum > 1<<20 {
		return nil, errClassifierFact
	}
	endpoint, err := url.Parse(target)
	if err != nil || !validFactURL(endpoint) {
		return nil, errClassifierFact
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if s.policyMu.LockContext(ctx) != nil {
		return nil, errClassifierFact
	}
	source, current := s.judgeSourceSnapshotLocked(ctx)
	if !current {
		s.policyMu.Unlock()
		return nil, errClassifierFact
	}
	done := make(chan struct{})
	flow := s.flows.addHTTPRequest(activeFlow{request: Request{Host: endpoint.Hostname(), Port: 443, Method: "GET", Path: endpoint.EscapedPath()}, http: true, judgeSource: source,
		secretNames: judgeSourceNames(source), done: done, cancel: cancel, check: func(_ *Engine, _ *Dialer) bool { return s.currentMembers() == source }})
	s.policyMu.Unlock()
	if flow == 0 {
		return nil, errClassifierFact
	}
	origin := newJudgeOrigin()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { origin.close(); close(closed) })
	var body io.ReadCloser
	var transport *http.Transport
	defer func() {
		cancel()
		origin.close()
		if body != nil {
			_ = body.Close()
		}
		if transport != nil {
			transport.CloseIdleConnections()
		}
		if !stop() {
			<-closed
		}
		s.flows.remove(flow)
		close(done)
	}()
	if facts.testFetch != nil {
		raw, err := facts.testFetch(ctx, target, maximum)
		if err != nil || int64(len(raw)) > maximum || ctx.Err() != nil || s.currentMembers() != source {
			return nil, errClassifierFact
		}
		return raw, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errClassifierFact
	}
	request.Close = true
	request.Header.Set("Accept", "application/rdap+json, application/json")
	transport = &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: maxHTTPHeaderBytes, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			if address != net.JoinHostPort(endpoint.Hostname(), strconv.Itoa(443)) || ctx.Err() != nil || s.currentMembers() != source || !origin.beginDial() {
				return nil, errClassifierFact
			}
			defer origin.dials.Done()
			addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", endpoint.Hostname())
			if err != nil || len(addresses) > 16 {
				return nil, errClassifierFact
			}
			dialer := &net.Dialer{Timeout: time.Second}
			for _, address := range addresses {
				address = address.Unmap()
				if _, allowed := s.dialer.CheckAddr(address, 443, nil); !allowed {
					continue
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), "443"))
				if err != nil {
					continue
				}
				if ctx.Err() != nil || s.currentMembers() != source {
					_ = conn.Close()
					return nil, errClassifierFact
				}
				return origin.attach(conn)
			}
			return nil, errClassifierFact
		}}
	if ctx.Err() != nil || s.currentMembers() != source {
		return nil, errClassifierFact
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		return nil, errClassifierFact
	}
	body = response.Body
	if response.StatusCode != http.StatusOK {
		return nil, errClassifierFact
	}
	raw, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil || int64(len(raw)) > maximum || ctx.Err() != nil || s.currentMembers() != source {
		return nil, errClassifierFact
	}
	return raw, nil
}
