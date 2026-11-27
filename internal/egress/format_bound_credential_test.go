package egress

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCallerOwnedBoundFormattingUsesTheExistingCodecAndHeaderBoundary(t *testing.T) {
	for _, test := range []struct{ format, want string }{{"{value}", "abcXYZ"}, {"Bearer {value}", "Bearer abcXYZ"}, {"basic:user:{value}", "Basic dXNlcjphYmNYWVo="}} {
		binding := api.SecretBind{Hosts: []string{"example.com:443"}, Header: "Authorization", Format: test.format}
		got, err := FormatBoundCredential(binding, "abcXYZ")
		if err != nil || got != test.want {
			t.Fatal("caller-owned formatter diverged from the existing raw, bearer or Basic codec", err)
		}
	}
	binding := api.SecretBind{Hosts: []string{"example.com:443"}, Header: strings.Repeat("X", maxHTTPHeaderBytes-5), Format: "{value}"}
	if _, err := FormatBoundCredential(binding, "A"); err != nil {
		t.Fatal("exact existing header limit was tightened", err)
	}
	binding.Header += "X"
	if value, err := FormatBoundCredential(binding, "A"); err == nil || value != "" {
		t.Fatal("oversized header escaped the shared representation bound")
	}
	for _, value := range []api.Secret{"", "bad\rvalue", "bad\nvalue", "bad\x00value"} {
		binding.Header = "Authorization"
		if formatted, err := FormatBoundCredential(binding, value); err == nil || formatted != "" {
			t.Fatal("invalid credential reached a formatted field")
		}
	}
	binding.Header = "Host"
	if value, err := FormatBoundCredential(binding, "abcXYZ"); err == nil || value != "" {
		t.Fatal("formatter admitted a routing header")
	}
}
