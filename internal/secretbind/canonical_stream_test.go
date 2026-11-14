package secretbind

import (
	"net/http"
	"strings"
	"testing"
)

func TestStoredHeaderValidationSharesGrammarAndRequiresCanonicalSpelling(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Api-Key", "X-Custom-42", "x-api-key", "X-API-Key", "aUTHORIZATION", "Host", "Proxy-Foo", "X-" + strings.Repeat("a", 1<<16)} {
		permissive := NewHeaderValidator(t.Context())
		_, err := permissive.Write([]byte(header))
		if err == nil {
			err = permissive.Finish()
		}
		want := err == nil && http.CanonicalHeaderKey(header) == header
		for split := 0; split <= len(header); split++ {
			if len(header) > 1024 && split != 0 && split != len(header) && split != len(header)/2 {
				continue
			}
			strict := NewCanonicalHeaderValidator(t.Context())
			_, got := strict.Write([]byte(header[:split]))
			if got == nil {
				_, got = strict.Write([]byte(header[split:]))
			}
			if got == nil {
				got = strict.Finish()
			}
			if (got == nil) != want {
				t.Fatal("stored header validator broadened grammar or accepted a noncanonical alias")
			}
		}
	}
}

func TestStoredAuthorityValidationRequiresCanonicalExplicitHTTPSPort(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"api.example.com:443", true}, {"127.0.0.1:443", true}, {"[2001:db8::1]:443", true},
		{"api.example.com", false}, {"API.example.com:443", false}, {"api.example.com.:443", false},
		{" api.example.com:443", false}, {"api.example.com:443\u2000", false}, {"api.example.com:0443", false},
		{"api.example.com:80", false}, {"*.example.com:443", false}, {"api.example.com/path", false},
	} {
		for split := 0; split <= len(tc.raw); split++ {
			strict := NewCanonicalAuthorityValidator(t.Context())
			_, err := strict.Write([]byte(tc.raw[:split]))
			if err == nil {
				_, err = strict.Write([]byte(tc.raw[split:]))
			}
			var canonical string
			if err == nil {
				canonical, err = strict.Finish()
			}
			if (err == nil) != tc.valid || tc.valid && canonical != tc.raw {
				t.Fatal("stored authority accepted an alias or changed its exact canonical representation")
			}
		}
	}
}
