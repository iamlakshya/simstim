package api

import "testing"

func TestNormalizeSecretHost(t *testing.T) {
	valid := map[string]string{
		"api.anthropic.com":  "api.anthropic.com",
		"API.OpenAI.COM.":    "api.openai.com",
		"  sub-1.example.io": "sub-1.example.io",
	}
	for raw, want := range valid {
		got, err := NormalizeSecretHost(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeSecretHost(%q) = %q, %v; want %q, nil", raw, got, err, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"https://api.anthropic.com",
		"api.anthropic.com:443",
		"*.example.com",
		"api.example.com/v1",
		"bad host.com",
		"-bad.example.com",
		"bad-.example.com",
		"a..b.com",
	}
	for _, raw := range invalid {
		if got, err := NormalizeSecretHost(raw); err == nil {
			t.Errorf("NormalizeSecretHost(%q) = %q, nil; want error", raw, got)
		}
	}
}

func TestNormalizeGatewayRouteAndSlug(t *testing.T) {
	if got, err := NormalizeGatewayRoute(" /Anthropic/ "); err != nil || got != "anthropic" {
		t.Fatalf("NormalizeGatewayRoute = %q, %v", got, err)
	}
	if _, err := NormalizeGatewayRoute("a/b"); err == nil {
		t.Fatal("expected error for nested route slug")
	}
	if got := RouteSlugForHost("API.Example.COM."); got != "api-example-com" {
		t.Fatalf("RouteSlugForHost = %q", got)
	}
	if !ValidHeaderName("x-api-key") || !ValidHeaderName("Authorization") {
		t.Fatal("expected valid headers")
	}
	if ValidHeaderName("") || ValidHeaderName("bad header") || ValidHeaderName("bad:header") {
		t.Fatal("expected invalid headers rejected")
	}
}
