package egress

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func FuzzCanonicalHTTPRequestKeepsTheCheckedAuthorityAndPath(f *testing.F) {
	for _, seed := range []struct {
		method, target, host, header string
		h2                           bool
	}{
		{"GET", "https://api.example.com/items/%69d?q=value", "api.example.com", "", true},
		{"get", "/items/id", "API.example.com.", "", false},
		{"POST", "https://other.example.com/items", "api.example.com", "", true},
		{"GET", "/items/%252fsecret", "api.example.com", "", true},
		{"GET", "/items/../secret", "api.example.com", "", false},
		{"GET", "/items", "api.example.com", "other.example.com", true},
		{"CONNECT", "api.example.com:443", "api.example.com", "", false},
	} {
		f.Add(seed.method, seed.target, seed.host, seed.header, seed.h2)
	}
	f.Fuzz(func(t *testing.T, method, target, host, header string, h2 bool) {
		// The real server has this existing complete-header bound. Keep the
		// pure parser harness within that same input domain, without I/O.
		if len(method)+len(target)+len(host)+len(header) > maxHTTPHeaderBytes {
			return
		}
		parsed, err := url.Parse(target)
		if err != nil {
			return
		}
		request := &http.Request{Method: method, URL: parsed, Host: host, Header: make(http.Header), RequestURI: target, ProtoMajor: 1}
		if h2 {
			request.ProtoMajor = 2
			request.TLS = &tls.ConnectionState{ServerName: "api.example.com"}
		}
		if header != "" {
			request.Header.Set("Host", header)
		}
		checked, err := canonicalHTTPRequest(request, StreamHeader{Host: "api.example.com", Port: 443}, "https")
		if err != nil {
			return
		}
		if !api.ValidHTTPMethod(method) || method == http.MethodConnect || checked.Method != method || checked.Host != "api.example.com" || checked.Port != 443 {
			t.Fatal("canonical admission changed or broadened the checked method/authority")
		}
		if request.URL.Host != "api.example.com:443" || request.Host != request.URL.Host || request.URL.Scheme != "https" || request.URL.User != nil || request.URL.Opaque != "" || request.URL.Fragment != "" || request.URL.RawPath != "" || request.RequestURI != "" || len(request.URL.RawQuery) > maxHTTPQueryBytes {
			t.Fatal("forwarded HTTP form differs from the checked target")
		}
		if checked.Path != request.URL.Path || !strings.HasPrefix(checked.Path, "/") {
			t.Fatal("forwarded path differs from checked path")
		}
		canonical, err := canonicalPath(request.URL.EscapedPath())
		if err != nil || canonical != checked.Path {
			t.Fatal("accepted HTTP path is not stable under canonical validation")
		}
	})
}
