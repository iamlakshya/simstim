package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

var errJudgeTransport = errors.New("configured judge endpoint or bound key is unavailable. Try: check trusted classifier settings and arm the original endpoint's bound credential")

type judgeModelCapability struct {
	server   *Server
	endpoint url.URL
	request  Request
	settings api.ClassifierValues
	key      string
	// testRoundTrip is set only by package tests; constructors leave it nil.
	testRoundTrip func(*http.Request) (*http.Response, error)
	// Package tests can supply a local pipe and its trust root at the same
	// guarded factory. Production constructors leave both hooks nil.
	testOriginDial  func(context.Context, string, string) (net.Conn, error)
	testOriginRoots *x509.CertPool
}

func (*judgeModelCapability) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}

// configuredJudge creates no process and performs no origin IO. Invocation
// owns the original actor, deadline, rate, input source check and late-rule
// transaction. The model capability retains only original approved metadata.
func (s *Server) configuredJudge(_ context.Context, policy api.Policy) (classify.Judge, error) {
	if err := CheckClassifierConfigurationV1(policy); err != nil {
		return nil, err
	}
	if !trustedJudgeSettings(policy) {
		return nil, errJudgeTransport
	}
	values := api.ClonePolicy(policy).Classifier
	if values.Judge == nil || *values.Judge != "model" {
		return classify.NewConfigured(values, "", nil)
	}
	if values.Endpoint == nil {
		return nil, errJudgeTransport
	}
	endpoint, checked, protocol, err := judgeEndpoint(*values.Endpoint)
	if err != nil {
		return nil, err
	}
	key := ""
	if values.Key != nil {
		key = *values.Key
	}
	if key != "" && (endpoint.Scheme != "https" || checked.Port != 443 || !validRegistryName(key)) {
		return nil, errJudgeTransport
	}
	capability := &judgeModelCapability{server: s, endpoint: endpoint, request: checked, settings: values, key: key}
	return classify.NewConfigured(values, protocol, capability)
}

func trustedJudgeSettings(policy api.Policy) bool {
	if policy.Classifier.Judge == nil || *policy.Classifier.Judge == "none" {
		return true
	}
	for _, origin := range policy.Origins {
		if origin.Source == api.SourceRepositoryFile && strings.HasPrefix(origin.Key, "classifier.") {
			switch origin.Key {
			case "classifier.judge", "classifier.endpoint", "classifier.model", "classifier.key", "classifier.command":
				return false
			}
		}
	}
	return true
}

func judgeEndpoint(raw string) (url.URL, Request, classify.ModelProtocol, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Scheme != "https" && endpoint.Scheme != "http" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return url.URL{}, Request{}, "", errJudgeTransport
	}
	host, err := NormaliseHost(endpoint.Hostname())
	if err != nil {
		return url.URL{}, Request{}, "", errJudgeTransport
	}
	port := 443
	if endpoint.Scheme == "http" {
		port = 80
	}
	if explicit := endpoint.Port(); explicit != "" {
		port, err = strconv.Atoi(explicit)
		if err != nil || port < 1 || port > 65535 {
			return url.URL{}, Request{}, "", errJudgeTransport
		}
	}
	path, err := canonicalPath(endpoint.EscapedPath())
	if err != nil {
		return url.URL{}, Request{}, "", errJudgeTransport
	}
	var protocol classify.ModelProtocol
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		protocol = classify.OpenAIChat
	case strings.HasSuffix(path, "/messages"):
		protocol = classify.AnthropicMessage
	default:
		return url.URL{}, Request{}, "", errJudgeTransport
	}
	endpoint.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return *endpoint, Request{Host: host, Port: port, Method: http.MethodPost, Path: path}, protocol, nil
}

func (capability *judgeModelCapability) currentSettings() bool {
	state := capability.server.currentMembers()
	if state == nil || !trustedJudgeSettings(state.metadata.Policy) || !reflect.DeepEqual(state.metadata.Policy.Classifier, capability.settings) || capability.settings.Endpoint == nil {
		return false
	}
	endpoint, checked, _, err := judgeEndpoint(*capability.settings.Endpoint)
	key := ""
	if capability.settings.Key != nil {
		key = *capability.settings.Key
	}
	return err == nil && endpoint.String() == capability.endpoint.String() && reflect.DeepEqual(checked, capability.request) && key == capability.key
}

// Complete is the sole model IO boundary. The caller supplies only a bounded
// API payload; it cannot name an endpoint, key, header, URL or redirect.
func (capability *judgeModelCapability) Complete(ctx context.Context, payload []byte) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || len(payload) == 0 || len(payload) > 2*classify.MaxInputBytes {
		return nil, errJudgeTransport
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, capability.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errJudgeTransport
	}
	request.Close = true
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if strings.HasSuffix(capability.request.Path, "/messages") {
		request.Header.Set("Anthropic-Version", "2023-06-01")
	}
	s := capability.server
	if err := s.policyMu.LockContext(ctx); err != nil {
		return nil, errJudgeTransport
	}
	source, sourceCurrent := s.judgeSourceSnapshotLocked(ctx)
	if !sourceCurrent || !capability.currentSettings() {
		s.policyMu.Unlock()
		return nil, errJudgeTransport
	}
	// Configuration is part of the fixed foreign payload too. The public
	// input DTO check does not cover a model label or endpoint path.
	if capability.settings.Model == nil || cleanAuditText(*capability.settings.Model, s.scrubber.snapshot(), maxScrubInputBytes) != *capability.settings.Model || cleanAuditText(capability.endpoint.String(), s.scrubber.snapshot(), maxScrubInputBytes) != capability.endpoint.String() {
		s.policyMu.Unlock()
		return nil, errJudgeTransport
	}
	if err := s.applyTrustedJudgeHeadersLocked(ctx, request, capability); err != nil {
		s.policyMu.Unlock()
		return nil, err
	}
	wire, path := "openai-chat-completions", "/v1/chat/completions"
	if strings.HasSuffix(capability.request.Path, "/messages") {
		wire, path = "anthropic-messages", "/v1/messages"
	}
	usage, refusal, usageErr := s.usage.BeginV1(ModelUsageRequestV1{Wire: wire, Path: path, Model: *capability.settings.Model, RequestBytes: uint64(len(payload))}, modelBudget(source.metadata.Policy))
	if usageErr != nil || refusal != nil {
		s.policyMu.Unlock()
		s.emitModelBudget(ctx, refusal)
		return nil, errJudgeTransport
	}
	usage.setContext(ctx)
	usageComplete := false
	defer func() { usage.FinishV1(usageComplete) }()
	done := make(chan struct{})
	names := judgeSourceNames(source)
	flow := s.flows.addHTTPRequest(activeFlow{request: capability.request, http: true, secretNames: names, done: done, cancel: cancel, judgeSource: source,
		check: func(_ *Engine, _ *Dialer) bool { return s.currentMembers() == source && capability.currentSettings() }})
	if flow == 0 {
		s.policyMu.Unlock()
		return nil, errJudgeTransport
	}
	s.policyMu.Unlock()
	origin := newJudgeOrigin()
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() { origin.close(); close(closed) })
	var transport *http.Transport
	var responseBody io.ReadCloser
	var physicalClient *http.ClientConn
	var physicalUpload *physicalH2UploadConn
	defer func() {
		cancel()
		origin.close()
		if physicalUpload != nil {
			_ = physicalUpload.Close()
		}
		if physicalClient != nil {
			_ = physicalClient.Close()
		}
		if responseBody != nil {
			_ = responseBody.Close()
		}
		if transport != nil {
			transport.CloseIdleConnections()
		}
		if !stopClose() {
			<-closed
		}
		// The request and private header are released, never mutated while
		// net/http may retain a reader after a RoundTrip error.
		usage.FinishV1(usageComplete)
		s.flows.remove(flow)
		close(done)
	}()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	dialOrigin := dialer.DialContext
	if capability.testOriginDial != nil {
		dialOrigin = capability.testOriginDial
	}
	trust := &tls.Config{MinVersion: tls.VersionTLS12}
	if capability.testOriginRoots != nil {
		trust.RootCAs = capability.testOriginRoots.Clone()
	}
	transport = &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		MaxResponseHeaderBytes: maxHTTPHeaderBytes, ResponseHeaderTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second,
		TLSClientConfig: trust,
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			if address != capability.endpoint.Host || ctx.Err() != nil || s.currentMembers() != source || !capability.currentSettings() {
				return nil, errJudgeTransport
			}
			if !origin.beginDial() {
				return nil, errJudgeTransport
			}
			defer origin.dials.Done()
			conn, err := dialOrigin(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if ctx.Err() != nil || s.currentMembers() != source {
				_ = conn.Close()
				return nil, errJudgeTransport
			}
			return origin.attach(conn)
		}}
	// RoundTrip follows no redirects and uses no environment proxy. The
	// standard TLS verifier pins the original configured hostname as usual.
	if ctx.Err() != nil || s.currentMembers() != source || !capability.currentSettings() {
		return nil, errJudgeTransport
	}
	var response *http.Response
	if capability.testRoundTrip != nil {
		response, err = capability.testRoundTrip(request)
	} else {
		response, physicalClient, physicalUpload, err = roundTripPhysicalJudgeOrigin(request, transport, usage, capability.request.Host, func(context.Context) bool {
			return ctx.Err() == nil && s.currentMembers() == source && capability.currentSettings()
		})
	}
	if err != nil {
		return nil, errJudgeTransport
	}
	responseBody = response.Body
	if response.StatusCode != http.StatusOK {
		return nil, errJudgeTransport
	}
	decoded, _, decodeErr := decodeHTTPBody(response.Body, response.Header)
	if decodeErr != nil {
		return nil, errJudgeTransport
	}
	responseBody = decoded
	usage.ResponseV1(ModelUsageJSONV1)
	answer, err := io.ReadAll(io.LimitReader(decoded, classify.MaxModelAnswerBytes+1))
	usage.ForwardedV1(answer)
	if err != nil || len(answer) > classify.MaxModelAnswerBytes || ctx.Err() != nil {
		return nil, errJudgeTransport
	}
	usageComplete = true
	return answer, nil
}
