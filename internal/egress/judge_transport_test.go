package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func judgeSettings() api.ClassifierValues {
	kind, endpoint, model, key := "model", "https://example.com/v1/chat/completions", "fixture-model", "KEY"
	return api.ClassifierValues{Judge: &kind, Endpoint: &endpoint, Model: &model, Key: &key}
}

func judgeTrustedUse(settings api.ClassifierValues) api.TrustedSecretUseV1 {
	return api.TrustedSecretUseV1{
		Version:  1,
		Use:      api.TrustedJudgeUseV1,
		Mode:     api.SecretModeBound,
		Bind:     api.SecretBind{Hosts: []string{"example.com:443"}, Header: "Authorization", Format: "Bearer {value}"},
		Model:    *settings.Model,
		Endpoint: *settings.Endpoint,
	}
}

func judgeCapabilityFixture(t *testing.T) (*Server, *judgeModelCapability) {
	t.Helper()
	return judgeCapabilityFixtureWithClock(t, nil)
}

func judgeCapabilityFixtureWithClock(t *testing.T, clock func() time.Time) (*Server, *judgeModelCapability) {
	t.Helper()
	settings := judgeSettings()
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	args.Initial.Policy.Order = []api.LayerRef{{Kind: api.LayerAll}}
	args.Initial.Policy.Classifier = settings
	args.TrustedUses = map[string]api.TrustedSecretUseV1{"KEY": judgeTrustedUse(settings)}
	args.ProtectedTrustedSecretValues = map[string]api.Secret{"KEY": "ABCD0000"}
	server := newBoundControlFixtureWithOptions(t, true, Options{Now: clock}).server
	if _, err := server.initialiseBoundControl(memberJSON(t, args)); err != nil {
		t.Fatal(err)
	}
	endpoint, checked, _, err := judgeEndpoint(*args.Initial.Policy.Classifier.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return server, &judgeModelCapability{server: server, endpoint: endpoint, request: checked, settings: api.ClonePolicy(args.Initial.Policy).Classifier, key: "KEY"}
}

func judgeSourceContext(t *testing.T, server *Server) context.Context {
	t.Helper()
	server.policyMu.Lock()
	defer server.policyMu.Unlock()
	return server.withJudgeSourceSnapshotLocked(t.Context())
}

func TestJudgeEndpointKeepsOriginalConfiguredAuthorityAndRejectsDecorations(t *testing.T) {
	for _, raw := range []string{
		"https://user:pass@example.com/v1/chat/completions", "https://example.com/v1/chat/completions?key=value",
		"https://example.com/v1/chat/completions#fragment", "https://example.com/v1/chat/completions?",
		"ftp://example.com/v1/chat/completions", "https://example.com/other",
	} {
		if _, _, _, err := judgeEndpoint(raw); err == nil {
			t.Fatalf("accepted decorated endpoint %q", raw)
		}
	}
	endpoint, checked, protocol, err := judgeEndpoint("http://127.0.0.1:11434/v1/chat/completions")
	if err != nil || endpoint.Host != "127.0.0.1:11434" || checked.Port != 11434 || protocol != classify.OpenAIChat {
		t.Fatal("local model endpoint changed", err)
	}
}

func TestJudgeFactoryRejectsRepositoryExecutionAndLeavesNoneAsDefault(t *testing.T) {
	server, _ := judgeCapabilityFixture(t)
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: judgeSettings()}}
	policy.Origins = []api.ValueOrigin{{Key: "classifier.endpoint", Source: api.SourceRepositoryFile}}
	if _, err := server.configuredJudge(t.Context(), policy); err == nil {
		t.Fatal("repository selected trusted model endpoint")
	}
	none, err := server.configuredJudge(t.Context(), api.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := none.Decide(t.Context(), classify.Input{})
	if err != nil || answer.Action != "unsure" {
		t.Fatal("default enabled a judge")
	}
}

func TestJudgeModelUsesSamePrivateHeaderInjectorWithoutGrantingGuestAuthority(t *testing.T) {
	server, capability := judgeCapabilityFixture(t)
	called := false
	capability.testRoundTrip = func(request *http.Request) (*http.Response, error) {
		called = true
		if request.URL.String() != "https://example.com:443/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer ABCD0000" {
			t.Fatal("fixed endpoint or private header changed")
		}
		if len(server.flows.snapshot()) != 1 {
			t.Fatal("judge request lacks lifetime registration")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`))}, nil
	}
	if _, err := capability.Complete(judgeSourceContext(t, server), []byte(`{"model":"fixture-model"}`)); err != nil || !called {
		t.Fatal("bound model request failed", err)
	}
	if len(server.flows.snapshot()) != 0 || server.currentEngine().Decide(Request{Host: "example.com", Port: 443}).Allow {
		t.Fatal("judge retained a request or granted guest network authority")
	}
}

func TestJudgeModelRefusesUnarmedReboundAndChangedOriginalSettingsBeforeIO(t *testing.T) {
	for _, mutate := range []func(*Server, *judgeModelCapability){
		func(server *Server, _ *judgeModelCapability) {
			state := server.currentMembers()
			secret := state.secrets["KEY"]
			secret.present = false
			state.secrets["KEY"] = secret
		},
		func(_ *Server, capability *judgeModelCapability) {
			capability.endpoint.Host = "elsewhere.example:443"
			capability.request.Host = "elsewhere.example"
		},
		func(server *Server, _ *judgeModelCapability) {
			changed := "changed-model"
			server.currentMembers().metadata.Policy.Classifier.Model = &changed
		},
	} {
		server, capability := judgeCapabilityFixture(t)
		ctx := judgeSourceContext(t, server)
		mutate(server, capability)
		capability.testRoundTrip = func(*http.Request) (*http.Response, error) {
			t.Fatal("refused model reached origin IO")
			return nil, nil
		}
		if _, err := capability.Complete(ctx, []byte(`{}`)); err == nil {
			t.Fatal("changed model capability succeeded")
		}
	}
}

func TestJudgeModelCancellationPhysicallyFinishesOwnedCall(t *testing.T) {
	server, capability := judgeCapabilityFixture(t)
	started := make(chan struct{})
	capability.testRoundTrip = func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, errors.New("cancelled")
	}
	done := make(chan error, 1)
	ctx := judgeSourceContext(t, server)
	go func() { _, err := capability.Complete(ctx, []byte(`{}`)); done <- err }()
	<-started
	server.flows.closeAll()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled model succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled model did not finish")
	}
	if len(server.flows.snapshot()) != 0 {
		t.Fatal("cancelled model left an owned request")
	}
}

func TestJudgeModelRefusesRedirectAndOversizedEnvelope(t *testing.T) {
	for _, response := range []*http.Response{
		{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"https://elsewhere.example/"}}, Body: io.NopCloser(strings.NewReader("redirect"))},
		{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", classify.MaxModelAnswerBytes+1)))},
	} {
		server, capability := judgeCapabilityFixture(t)
		capability.testRoundTrip = func(*http.Request) (*http.Response, error) { return response, nil }
		if _, err := capability.Complete(judgeSourceContext(t, server), []byte(`{}`)); err == nil {
			t.Fatal("redirect or oversized answer succeeded")
		}
	}
}

func TestJudgeModelRefusesMissingOrChangedSourceBeforeOriginIO(t *testing.T) {
	for _, field := range []string{"host", "task"} {
		server, capability := judgeCapabilityFixture(t)
		capability.testRoundTrip = func(*http.Request) (*http.Response, error) { t.Fatal("stale source reached model IO"); return nil, nil }
		if _, err := capability.Complete(t.Context(), []byte(`{}`)); err == nil {
			t.Fatal("missing private source capability succeeded")
		}
		ctx := judgeSourceContext(t, server)
		// The original DTO source gate has already run. A real arm changes a
		// credential to the host or task before Complete reacquires the gate.
		value := api.Secret("WXYZ1111")
		if field == "host" {
			// A fresh source uses a lowercase host-shaped credential cohort.
			settings := judgeSettings()
			args := boundRegistryArgs(map[string]string{"KEY": "abcd0000"})
			args.Initial.Policy.Order = []api.LayerRef{{Kind: api.LayerAll}}
			args.Initial.Policy.Classifier = settings
			args.TrustedUses = map[string]api.TrustedSecretUseV1{"KEY": judgeTrustedUse(settings)}
			args.ProtectedTrustedSecretValues = map[string]api.Secret{"KEY": "abcd0000"}
			server, _ = newBoundRegistryServer(t, args)
			capability.server = server
			ctx = judgeSourceContext(t, server)
			value = "wxyz1111"
		}
		generation := server.currentMembers().generation
		if _, err := server.armControl(memberJSON(t, ArmArgs{Version: MemberRegistryV2, Generation: generation, Values: map[string]api.Secret{"KEY": value}})); err != nil {
			t.Fatal(err)
		}
		payload := []byte(`{"` + field + `":"` + string(value) + `"}`)
		if _, err := capability.Complete(ctx, payload); err == nil {
			t.Fatal("new current value entered model input through a stale source snapshot")
		}
	}
}

func TestJudgeModelUnrelatedCredentialArmCancelsAndJoinsBeforeReplacement(t *testing.T) {
	settings := judgeSettings()
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000", "OTHER_KEY": "EFGH2222"})
	args.Binds["OTHER_KEY"] = api.SecretBind{Hosts: []string{"other.example"}, Header: "Authorization", Format: "Bearer {value}"}
	args.Initial.Policy.Order = []api.LayerRef{{Kind: api.LayerAll}}
	args.Initial.Policy.Classifier = settings
	args.TrustedUses = map[string]api.TrustedSecretUseV1{"KEY": judgeTrustedUse(settings)}
	args.ProtectedTrustedSecretValues = map[string]api.Secret{"KEY": "ABCD0000"}
	server, _ := newBoundRegistryServer(t, args)
	endpoint, checked, _, err := judgeEndpoint(*args.Initial.Policy.Classifier.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	capability := &judgeModelCapability{server: server, endpoint: endpoint, request: checked, settings: args.Initial.Policy.Classifier, key: "KEY"}
	started := make(chan struct{})
	capability.testRoundTrip = func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, errors.New("cancelled")
	}
	ctx := judgeSourceContext(t, server)
	callDone := make(chan error, 1)
	go func() { _, err := capability.Complete(ctx, []byte(`{}`)); callDone <- err }()
	<-started
	generation := server.currentMembers().generation
	armDone := make(chan error, 1)
	go func() {
		_, err := server.armControl(memberJSON(t, ArmArgs{Version: MemberRegistryV2, Generation: generation, Values: map[string]api.Secret{"OTHER_KEY": "IJKL3333"}}))
		armDone <- err
	}()
	select {
	case err := <-armDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated source credential arm did not join the model call")
	}
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("source-retired model call succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("retired model call did not physically finish")
	}
	if len(server.flows.snapshot()) != 0 {
		t.Fatal("source-retired model call retained lifetime state")
	}
}
