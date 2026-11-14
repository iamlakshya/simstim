package secretbind

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLiteralSecretBindingCanonicalisesWithoutChangingDestinationScope(t *testing.T) {
	input := api.SecretBind{Hosts: []string{"API.example.com.", "api.example.com:443", "192.0.2.8", "[2001:db8::1]:443"}, Header: "x-api-key", Format: "{value}"}
	want := api.SecretBind{Hosts: []string{"192.0.2.8:443", "[2001:db8::1]:443", "api.example.com:443"}, Header: "X-Api-Key", Format: "{value}"}
	got, err := Canonical(input)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("literal binding = %#v, %v", got, err)
	}
	got.Hosts[0] = "changed.example.com:443"
	if input.Hosts[0] != "API.example.com." {
		t.Fatal("canonical metadata aliases its input destination slice")
	}
}

func TestSecretBindingCannotBroadenToPatternsRangesOrOtherProtocols(t *testing.T) {
	for _, raw := range []string{
		"", "*", "*.example.com", "https://api.example.com", "api.example.com/path",
		"api.example.com?query", "api.example.com#fragment", "user@api.example.com",
		"api.example.com:80", "api.example.com:8443", "192.0.2.0/24", "192.0.2.8/32",
		"[2001:db8::1]/128", "api.example.com\r\nother.example.com", "api.example.com:443/",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Canonical(api.SecretBind{Hosts: []string{raw}, Header: "Authorization", Format: "Bearer {value}"}); err == nil {
				t.Fatal("a binding became a network pattern, non-HTTPS destination or malformed authority")
			}
		})
	}
}

func TestCredentialBindingsCannotControlHTTPRoutingFramingOrProxyState(t *testing.T) {
	for _, header := range []string{
		"", "Host", "host", "Content-Length", "Transfer-Encoding", "Trailer", "TE", "Connection",
		"Upgrade", "Keep-Alive", "Forwarded", "X-Forwarded-Host", "Proxy-Authorization", "Proxy-Connection",
		"Accept-Encoding", "Content-Encoding", "Content-Type",
		":authority", "bad header", "X-Key\r\nInjected", "Authorization\x00",
	} {
		t.Run(header, func(t *testing.T) {
			if _, err := Canonical(api.SecretBind{Hosts: []string{"api.example.com"}, Header: header, Format: "{value}"}); err == nil {
				t.Fatal("credential metadata gained routing or framing authority")
			}
		})
	}
	for _, header := range []string{"Authorization", "x-api-key", "Api-Key", "X-Provider-Credential"} {
		if _, err := Canonical(api.SecretBind{Hosts: []string{"api.example.com"}, Header: header, Format: "{value}"}); err != nil {
			t.Fatalf("literal named credential header refused: %v", err)
		}
	}
}

func TestCredentialFormatHasOneExactValuePosition(t *testing.T) {
	for _, tc := range []struct{ input, kind, user string }{
		{"{value}", "raw", ""}, {"Bearer {value}", "bearer", ""},
		{"basic:x-access-token:{value}", "basic", "x-access-token"}, {"basic::{value}", "basic", ""},
	} {
		kind, user, err := ParseFormat(tc.input)
		if err != nil || kind != tc.kind || user != tc.user {
			t.Fatalf("format grammar changed: %q, %q, %v", kind, user, err)
		}
	}
	for _, input := range []string{"", "bearer {value}", "Bearer {value}{value}", "Token {value}", "basic:name:extra:{value}", "basic:user\r\n:{value}", "basic:user\x01:{value}", "basic:user\x7f:{value}", "basic:{value}:{value}"} {
		if _, _, err := ParseFormat(input); err == nil {
			t.Fatal("an unsupported or ambiguous credential format was accepted")
		}
	}
}

func TestBindingErrorsNeverEchoRejectedInput(t *testing.T) {
	const privateInput = "fixture-sensitive-input"
	for _, input := range []api.SecretBind{
		{Hosts: []string{"api.example.com/" + privateInput}, Header: "x-api-key", Format: "{value}"},
		{Hosts: []string{"api.example.com"}, Header: "X-Key\r\n" + privateInput, Format: "{value}"},
		{Hosts: []string{"api.example.com"}, Header: "x-api-key", Format: privateInput},
	} {
		_, err := Canonical(input)
		if err == nil || strings.Contains(err.Error(), privateInput) {
			t.Fatal("binding validation exposed rejected input or failed to reject it")
		}
	}
}
