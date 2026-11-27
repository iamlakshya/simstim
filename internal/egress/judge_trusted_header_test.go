package egress

import (
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

func trustedJudgeHeaderFixture(t *testing.T) (*Server, *judgeModelCapability) {
	t.Helper()
	server, capability := judgeCapabilityFixture(t)
	state := server.currentMembers()
	secret := state.secrets[capability.key]
	secret.trusted = &api.TrustedSecretUseV1{Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: secret.bound.binding, Model: *capability.settings.Model, Endpoint: *capability.settings.Endpoint}
	secret.guest, secret.bound, secret.trustedArmed = false, nil, true
	state.secrets[capability.key] = secret
	return server, capability
}

func TestTrustedJudgeHeaderUsesSameRegistryWithoutGuestSentinel(t *testing.T) {
	server, capability := trustedJudgeHeaderFixture(t)
	request, err := http.NewRequest(http.MethodPost, capability.endpoint.String(), strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := judgeSourceContext(t, server)
	if err := server.policyMu.LockContext(ctx); err != nil {
		t.Fatal(err)
	}
	err = server.applyTrustedJudgeHeadersLocked(ctx, request, capability)
	server.policyMu.Unlock()
	secret := server.currentMembers().secrets[capability.key]
	kind, user, _ := secretbind.ParseFormat(secret.trusted.Bind.Format)
	want, _ := requestCredentialValue(kind, user, secret.value)
	if err != nil || request.Header.Get(secret.trusted.Bind.Header) != string(want) || secret.bound != nil || secret.guest {
		t.Fatal("trusted role depended on a guest sentinel or another registry", err)
	}
}

func TestTrustedJudgeHeaderRefusesMissingRoleUnarmedAndChangedBinding(t *testing.T) {
	for _, mutate := range []func(*registeredSecret, *judgeModelCapability){
		func(secret *registeredSecret, _ *judgeModelCapability) { secret.trusted = nil },
		func(secret *registeredSecret, _ *judgeModelCapability) { secret.present = false },
		func(secret *registeredSecret, _ *judgeModelCapability) { secret.trustedArmed = false },
		func(secret *registeredSecret, _ *judgeModelCapability) { secret.trusted.Model = "another-model" },
		func(secret *registeredSecret, _ *judgeModelCapability) {
			secret.trusted.Endpoint = "https://another.example/v1/chat/completions"
		},
		func(secret *registeredSecret, _ *judgeModelCapability) { secret.trusted.Use = "guest" },
		func(secret *registeredSecret, _ *judgeModelCapability) {
			secret.trusted.Bind.Hosts = []string{"another.example:443"}
		},
	} {
		server, capability := trustedJudgeHeaderFixture(t)
		state := server.currentMembers()
		secret := state.secrets[capability.key]
		mutate(&secret, capability)
		state.secrets[capability.key] = secret
		ctx := judgeSourceContext(t, server)
		request, err := http.NewRequest(http.MethodPost, capability.endpoint.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.policyMu.LockContext(ctx); err != nil {
			t.Fatal(err)
		}
		err = server.applyTrustedJudgeHeadersLocked(ctx, request, capability)
		server.policyMu.Unlock()
		if err == nil || len(request.Header) != 0 {
			t.Fatal("invalid trusted role wrote a private header")
		}
	}
}

func TestTrustedJudgeHeaderRequiresOriginalSourceAndExactRequest(t *testing.T) {
	server, capability := trustedJudgeHeaderFixture(t)
	for _, target := range []string{capability.endpoint.String(), "https://another.example/v1/chat/completions"} {
		request, err := http.NewRequest(http.MethodPost, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.policyMu.LockContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		err = server.applyTrustedJudgeHeadersLocked(t.Context(), request, capability)
		server.policyMu.Unlock()
		if err == nil || len(request.Header) != 0 {
			t.Fatal("unissued source or wrong endpoint obtained a header")
		}
	}
}

func TestTrustedJudgeHeaderPreservesSeparatelyApprovedExposedGuestRole(t *testing.T) {
	server, capability := trustedJudgeHeaderFixture(t)
	state := server.currentMembers()
	secret := state.secrets[capability.key]
	secret.mode, secret.guest = api.SecretModeExposed, true
	state.secrets[capability.key] = secret
	ctx := judgeSourceContext(t, server)
	request, err := http.NewRequest(http.MethodPost, capability.endpoint.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.policyMu.LockContext(ctx); err != nil {
		t.Fatal(err)
	}
	err = server.applyTrustedJudgeHeadersLocked(ctx, request, capability)
	server.policyMu.Unlock()
	if err != nil || request.Header.Get(secret.trusted.Bind.Header) == "" || !secret.guest || secret.mode != api.SecretModeExposed {
		t.Fatal("separate guest exposure prevented trusted bound use", err)
	}
}
