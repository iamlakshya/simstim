package secretbind

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func finishAuthorityParts(v *AuthorityValidator, parts ...string) (string, error) {
	for _, part := range parts {
		if _, err := v.Write([]byte(part)); err != nil {
			return "", err
		}
	}
	return v.Finish()
}

func TestAuthorityStreamAllBoundaries(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"DNS", "example.com", "example.com:443"},
		{"case and dot", "EXAMPLE.com.", "example.com:443"},
		{"explicit port", "example.com.:443", "example.com:443"},
		{"IPv4", "192.0.2.1", "192.0.2.1:443"},
		{"IPv6", "[2001:db8::1]:443", "[2001:db8::1]:443"},
		{"mapped IPv6", "[::ffff:192.0.2.1]", "192.0.2.1:443"},
		{"Unicode trim", "\u2003\u0085EXAMPLE.com.\u3000\u00a0", "example.com:443"},
		{"empty", "", ""},
		{"only whitespace", "\t\u2003\n", ""},
		{"interior ASCII space", "example .com", ""},
		{"interior Unicode space", "example\u2003.com", ""},
		{"space before port", "example.com :443", ""},
		{"non ASCII DNS", "\u00e9xample.com", ""},
		{"Cf is not trim space", "\ufeffexample.com", ""},
		{"invalid UTF8", "\xffexample.com", ""},
		{"truncated trim rune", "example.com\xc2", ""},
		{"overlong UTF8", "\xc0\xa0example.com", ""},
		{"wildcard", "*.example.com", ""},
		{"URL", "https://example.com", ""},
		{"path", "example.com/private", ""},
		{"empty URL path", "example.com/", ""},
		{"query", "example.com?query", ""},
		{"fragment", "example.com#fragment", ""},
		{"range", "192.0.2.0/24", ""},
		{"single address CIDR", "192.0.2.1/32", ""},
		{"other port", "example.com:80", ""},
		{"padded port", "example.com:0443", ""},
		{"signed port", "example.com:+443", ""},
		{"userinfo", "account@example.com", ""},
		{"zone", "[fe80::1%zone]:443", ""},
		{"extra trailing dot", "example.com..", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			material, materialErr := Canonical(api.SecretBind{Header: "Authorization", Format: "{value}", Hosts: []string{tc.raw}})
			if (materialErr == nil) != (tc.want != "") {
				t.Fatal("materialised authority acceptance differs from labelled grammar")
			}
			if materialErr == nil && (len(material.Hosts) != 1 || material.Hosts[0] != tc.want) {
				t.Fatal("materialised authority was not canonicalised")
			}
			for split := 0; split <= len(tc.raw); split++ {
				got, err := finishAuthorityParts(NewAuthorityValidator(context.Background()), tc.raw[:split], tc.raw[split:])
				if (err == nil) != (tc.want != "") || got != tc.want {
					t.Fatalf("authority acceptance or canonical form differs at byte boundary %d", split)
				}
			}
		})
	}
}

func TestAuthorityStreamExistingDNSBoundary(t *testing.T) {
	host := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(host) != 253 {
		t.Fatal("invalid intrinsic DNS boundary fixture")
	}
	raw := host + ".:443"
	for split := 0; split <= len(raw); split++ {
		got, err := finishAuthorityParts(NewAuthorityValidator(context.Background()), raw[:split], raw[split:])
		if err != nil || got != host+":443" {
			t.Fatalf("existing maximum DNS authority rejected at boundary %d: %v", split, err)
		}
	}
	for _, raw := range []string{host + "e:443", strings.Repeat("a", 64) + ".example:443", strings.Repeat("a", 259)} {
		if _, err := finishAuthorityParts(NewAuthorityValidator(context.Background()), raw); err == nil {
			t.Fatal("invalid overlong authority accepted")
		}
	}
}

func TestAuthorityStreamTrimSpaceExactly(t *testing.T) {
	// This is the standard Unicode whitespace set, including C1 NEL. It is
	// authority padding, not an expansion of Basic username controls.
	spaces := []rune{'\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xa0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007, 0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000}
	for _, space := range spaces {
		raw := string(space) + "example.com" + string(space)
		for split := 0; split <= len(raw); split++ {
			got, err := finishAuthorityParts(NewAuthorityValidator(context.Background()), raw[:split], raw[split:])
			if err != nil || got != "example.com:443" {
				t.Fatalf("standard trim rune U+%04X differs at byte boundary %d", space, split)
			}
		}
		_, err := finishAuthorityParts(NewAuthorityValidator(context.Background()), "example", string(space), ".com")
		if err == nil {
			t.Fatalf("interior whitespace U+%04X was accepted", space)
		}
	}
}

func TestAuthorityStreamUnlimitedTrimPadding(t *testing.T) {
	v := NewAuthorityValidator(context.Background())
	chunk := []byte(strings.Repeat("\u2003\t", 1024))
	for range 1024 {
		if _, err := v.Write(chunk); err != nil {
			t.Fatal("leading trim padding acquired a cap")
		}
	}
	if _, err := v.Write([]byte("example.com")); err != nil {
		t.Fatal(err)
	}
	for range 1024 {
		if _, err := v.Write(chunk); err != nil {
			t.Fatal("trailing trim padding acquired a cap")
		}
	}
	if got, err := v.Finish(); err != nil || got != "example.com:443" {
		t.Fatalf("padded authority did not canonicalise: %v", err)
	}
}

func TestAuthorityStreamTrimByteChunks(t *testing.T) {
	v := NewAuthorityValidator(context.Background())
	for _, b := range []byte("\u2003\u0085\u3000EXAMPLE.com.:443\u00a0\u205f") {
		if _, err := v.Write([]byte{b}); err != nil {
			t.Fatal("trim or literal split into single bytes was rejected")
		}
	}
	if got, err := v.Finish(); err != nil || got != "example.com:443" {
		t.Fatal("bytewise Unicode padding changed the canonical authority")
	}
}
