package egress

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func canonicalRequestFixture(t *testing.T, target string) *http.Request {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{Method: "GET", URL: u, Host: "api.example.com", Header: make(http.Header), RequestURI: target,
		ProtoMajor: 2, TLS: &tls.ConnectionState{ServerName: "api.example.com"}}
}

func TestCanonicalHTTPAuthorityAndQueryChecksBeforeOrigin(t *testing.T) {
	hdr := StreamHeader{Host: "api.example.com", Port: 443}
	for _, test := range []struct {
		name   string
		change func(*http.Request)
	}{
		{"different authority", func(r *http.Request) { r.Host = "other.example.com" }},
		{"hidden Host", func(r *http.Request) { r.Header.Set("Host", "other.example.com") }},
		{"empty explicit Host", func(r *http.Request) { r.Header["Host"] = []string{""} }},
		{"duplicate Host", func(r *http.Request) { r.Header["Host"] = []string{"api.example.com", "api.example.com"} }},
		{"absolute target", func(r *http.Request) { r.URL.Host = "other.example.com" }},
		{"userinfo", func(r *http.Request) { r.URL.User = url.User("guest") }},
		{"fragment", func(r *http.Request) { r.URL.Fragment = "hidden" }},
		{"scheme", func(r *http.Request) { r.URL.Scheme = "http" }},
		{"H2 scheme", func(r *http.Request) { r.TLS = nil }},
		{"query", func(r *http.Request) { r.URL.RawQuery = strings.Repeat("q", maxHTTPQueryBytes+1) }},
		{"encoded traversal", func(r *http.Request) { r.URL.Path = "/allowed/../outside" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := canonicalRequestFixture(t, "https://api.example.com/allowed")
			test.change(req)
			if _, err := canonicalHTTPRequest(req, hdr, "https"); err == nil {
				t.Fatal("ambiguous request passed canonical admission")
			}
		})
	}
	req := canonicalRequestFixture(t, "https://API.example.com/allowed?q=value")
	request, err := canonicalHTTPRequest(req, hdr, "https")
	if err != nil || request.Path != "/allowed" || req.URL.Host != "api.example.com:443" || req.Host != req.URL.Host || req.URL.RawQuery != "q=value" || req.RequestURI != "" {
		t.Fatalf("forwarded form differs from policy form: %+v, %+v, %v", request, req.URL, err)
	}
}
