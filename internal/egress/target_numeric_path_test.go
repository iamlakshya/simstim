package egress

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestNumericPathsKeepTheirHostAndPort(t *testing.T) {
	for _, target := range []string{
		"example.com:80/123", "example.com:80/123:456", "example.com/123",
		"*.example.com:8080/42",
	} {
		t.Run(target, func(t *testing.T) {
			got, err := ValidateRuleTarget(target)
			if err != nil || got != target {
				t.Fatalf("ValidateRuleTarget(%q) = %q, %v", target, got, err)
			}
			pol := pathTestPolicy(t, api.PresetLocked, target)
			eng, err := NewEngine("shell", pol)
			if err != nil {
				t.Fatal(err)
			}
			hostPort, path, err := splitTargetPath(target)
			if err != nil || path == "" {
				t.Fatalf("numeric path lost: %q, %q, %v", hostPort, path, err)
			}
			host, port, err := parseHostHeader(strings.TrimPrefix(hostPort, "*."), 443)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(hostPort, "*.") {
				host = "api.example.com"
			}
			if !eng.Decide(Request{Host: host, Port: port, Path: path}).Allow || eng.Decide(Request{Host: host, Port: port, Path: path + "0"}).Allow {
				t.Fatal("numeric path no longer has exact policy boundaries")
			}
		})
	}
}

func TestNumericPathFixRetainsCIDRValidation(t *testing.T) {
	for _, target := range []string{"198.51.100.0/24", "198.51.100.0/24:443", "2001:db8::/32", "[2001:db8::/32]:443"} {
		if _, err := ValidateRuleTarget(target); err != nil {
			t.Errorf("valid CIDR %q was rejected: %v", target, err)
		}
	}
	for _, target := range []string{"198.51.100.0/33", "198.51.100.0/24:0", "198.51.100.0/24:65536", "2001:db8::/129", "[2001:db8::/32]:0", "127.0.0.1:80/123", "[2001:db8::1]:80/123"} {
		if _, err := ValidateRuleTarget(target); err == nil {
			t.Errorf("invalid CIDR %q was accepted", target)
		}
	}
	for _, target := range []string{"127.0.0.1:80/123", "[::1]:8080/123", "[2001:db8::1]:80/123"} {
		if _, err := ValidateRuleTarget(target); err == nil || !strings.Contains(err.Error(), "an IP target cannot carry a URL path") {
			t.Errorf("IP path %q did not reach the IP-with-path rejection: %v", target, err)
		}
	}
}
