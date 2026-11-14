package secretbind

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

type scalarValidator interface {
	io.Writer
	Finish() error
}

func finishScalarParts(v scalarValidator, parts ...string) error {
	for _, part := range parts {
		if _, err := v.Write([]byte(part)); err != nil {
			return err
		}
	}
	return v.Finish()
}

func TestHeaderStreamAllBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		valid bool
	}{
		{"credential", "Authorization", true},
		{"custom", "X-Credential", true},
		{"token punctuation", "!#$%&'*+-.^_`|~", true},
		{"reserved extension is distinct", "Transfer-Encoding-Extra", true},
		{"prefix without separator is distinct", "ProxyCredential", true},
		{"empty", "", false},
		{"routing case", "hOsT", false},
		{"length", "Content-Length", false},
		{"encoding", "TRANSFER-ENCODING", false},
		{"trailer", "Trailer", false},
		{"te", "TE", false},
		{"connection", "Connection", false},
		{"upgrade", "Upgrade", false},
		{"keep alive", "Keep-Alive", false},
		{"forwarded", "Forwarded", false},
		{"accept encoding", "Accept-Encoding", false},
		{"content encoding", "Content-Encoding", false},
		{"content type", "Content-Type", false},
		{"proxy prefix", "pRoXy-Long-Credential", false},
		{"forwarded prefix", "x-FoRwArDeD-Credential", false},
		{"space", "X Credential", false},
		{"colon", "X:Credential", false},
		{"newline", "X-Credential\n", false},
		{"nul", "X-\x00Credential", false},
		{"non ASCII", "X-\u00e9", false},
		{"malformed UTF8", "X-\xff", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, materialErr := Canonical(api.SecretBind{Header: tc.raw, Format: "{value}", Hosts: []string{"example.com"}})
			if (materialErr == nil) != tc.valid {
				t.Fatal("materialised header acceptance differs from the labelled grammar")
			}
			for split := 0; split <= len(tc.raw); split++ {
				v := NewHeaderValidator(context.Background())
				err := finishScalarParts(v, tc.raw[:split], tc.raw[split:])
				if (err == nil) != tc.valid {
					t.Fatalf("header acceptance differs at byte boundary %d", split)
				}
			}
		})
	}
}

func TestHeaderStreamLongFieldHasNoNewCap(t *testing.T) {
	chunk := []byte(strings.Repeat("Credential", 1024))
	valid := NewHeaderValidator(context.Background())
	reserved := NewHeaderValidator(context.Background())
	if _, err := reserved.Write([]byte("X-Forwarded-")); err != nil {
		t.Fatal(err)
	}
	for range 1024 {
		for _, validator := range []*HeaderValidator{valid, reserved} {
			if n, err := validator.Write(chunk); n != len(chunk) || err != nil {
				t.Fatalf("long header rejected before scalar completion: count=%d error=%v", n, err)
			}
		}
	}
	if err := valid.Finish(); err != nil {
		t.Fatalf("long valid token field acquired a cap: %v", err)
	}
	if err := reserved.Finish(); err == nil {
		t.Fatal("long reserved-prefix field was accepted")
	}
}

func TestHeaderCanonicalOwnsAndDeduplicatesMetadata(t *testing.T) {
	input := api.SecretBind{Header: "x-credential", Format: "{value}", Hosts: []string{"EXAMPLE.com.", "example.com:443", "other.example"}}
	canonical, err := Canonical(input)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Header != "X-Credential" || fmt.Sprint(canonical.Hosts) != "[example.com:443 other.example:443]" {
		t.Fatal("binding was not canonicalised and deduplicated")
	}
	input.Hosts[0] = "changed.example"
	if canonical.Hosts[0] != "example.com:443" {
		t.Fatal("canonical binding aliases the source host list")
	}
	if _, err := Canonical(api.SecretBind{Header: "Authorization", Format: "{value}"}); err == nil {
		t.Fatal("binding without an authority was accepted")
	}
}
