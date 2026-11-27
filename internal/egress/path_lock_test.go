package egress

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestLockedPathDeniesContainNarrowerAllows(t *testing.T) {
	for _, tc := range []struct {
		name, deny, allow string
		locked            bool
	}{
		{"prefix child", "api.example.com/copilot_internal/", "api.example.com/copilot_internal/v2", true},
		{"prefix subtree", "api.example.com/copilot_internal/", "api.example.com/copilot_internal/v2/", true},
		{"same prefix", "api.example.com/copilot_internal/", "api.example.com/copilot_internal/", true},
		{"prefix sibling", "api.example.com/copilot_internal/", "api.example.com/copilot_internal2/v2", false},
		{"prefix parent", "api.example.com/copilot_internal/", "api.example.com/copilot_internal", false},
		{"exact same", "api.example.com/user", "api.example.com/user", true},
		{"exact sibling", "api.example.com/user", "api.example.com/users", false},
		{"exact child", "api.example.com/user", "api.example.com/user/repos", false},
		{"exact subtree", "api.example.com/user", "api.example.com/user/", false},
		{"wildcard host child", "*.example.com/copilot_internal/", "api.example.com/copilot_internal/v2", true},
		{"same wildcard host", "*.example.com/copilot_internal/", "*.example.com/copilot_internal/v2", true},
		{"narrower wildcard host", "*.example.com/copilot_internal/", "*.sub.example.com/copilot_internal/v2/", true},
		{"wildcard apex", "*.example.com/copilot_internal/", "example.com/copilot_internal/v2", false},
		{"wildcard path sibling", "*.example.com/copilot_internal/", "api.example.com/repos/", false},
		{"broader host wildcard", "api.example.com/copilot_internal/", "*.example.com/copilot_internal/v2", false},
		{"same port", "api.example.com:443/copilot_internal/", "api.example.com/copilot_internal/v2", true},
		{"different port", "api.example.com:443/copilot_internal/", "api.example.com:8443/copilot_internal/v2", false},
		{"all ports", "api.example.com/copilot_internal/", "api.example.com:8443/copilot_internal/v2", true},
		{"whole host deny", "api.example.com", "api.example.com/copilot_internal/v2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := mergePathLock(tc.deny, tc.allow)
			if !tc.locked {
				if err != nil {
					t.Fatalf("disjoint allow failed: %v", err)
				}
				return
			}
			var pe *proto.Error
			if !errors.As(err, &pe) || pe.Code != "policy_locked" || pe.Hint == "" {
				t.Fatalf("allow inside a locked deny returned %v, want policy_locked with a hint", err)
			}
		})
	}
}

func TestLockedPathDecisionsKeepExactAndPrefixBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, deny, allow, host, path string
		want                          bool
	}{
		{"prefix refuses child", "api.example.com/copilot_internal/", "api.example.com", "api.example.com", "/copilot_internal/v2", false},
		{"prefix allows sibling", "api.example.com/copilot_internal/", "api.example.com", "api.example.com", "/repos/o/r", true},
		{"exact refuses only itself", "api.example.com/user", "api.example.com", "api.example.com", "/user", false},
		{"exact allows sibling", "api.example.com/user", "api.example.com", "api.example.com", "/users", true},
		{"exact allows descendant", "api.example.com/user", "api.example.com", "api.example.com", "/user/repos", true},
		{"wildcard refuses child", "*.example.com/copilot_internal/", "*.example.com", "api.example.com", "/copilot_internal/v2", false},
		{"specific deny allows other host", "api.example.com/copilot_internal/", "*.example.com", "other.example.com", "/copilot_internal/v2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pol, _, err := mergePathLock(tc.deny, tc.allow)
			if err != nil {
				t.Fatal(err)
			}
			eng, err := NewEngine("shell", pol)
			if err != nil {
				t.Fatal(err)
			}
			got := eng.Decide(Request{Host: tc.host, Port: 443, Path: tc.path})
			if got.Allow != tc.want {
				t.Fatalf("Decide(%q, %q) = %+v, want allow=%v", tc.host, tc.path, got, tc.want)
			}
		})
	}
}

func mergePathLock(deny, allow string) (api.Policy, []api.Layer, error) {
	return MergeLayers([]api.Layer{
		{Kind: api.LayerHost, Lock: &api.Lock{Deny: []string{deny}}},
		{Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: allow}}},
	})
}
