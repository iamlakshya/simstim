//go:build !windows

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/join/code"
)

// These tests use only native ssh -G on frozen local files. They open no
// network connection, listener, account or SSH daemon.
func TestNativeJoinedRouteNestedAndExplicitPrefixSemantics(t *testing.T) {
	if _, err := exec.LookPath("/usr/bin/ssh"); err != nil {
		t.Skip("native OpenSSH is unavailable")
	}
	for _, test := range []struct {
		name, config string
		want         []string
	}{
		{"nested", "Host box\n ProxyJump inner\nHost inner\n ProxyJump outer\nHost outer\n ProxyJump edge\n", []string{"edge", "outer", "inner"}},
		{"prefix-overrides-later", "Host box\n ProxyJump outer,inner\nHost inner\n ProxyJump ignored\n", []string{"outer", "inner"}},
		{"first-prefix-can-nest", "Host box\n ProxyJump outer,inner\nHost outer\n ProxyJump edge\nHost inner\n ProxyJump ignored\n", []string{"edge", "outer", "inner"}},
		{"ssh-uri-prefix", "Host box\n ProxyJump ssh://alice@outer:2201,inner\n", []string{"outer", "inner"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inviteHome(t)
			home := routeReadsetHomeV1(t)
			t.Setenv("PATH", "/usr/bin:/bin")
			path := filepath.Join(home, "selected.conf")
			text := test.config + "Host *\n User fixture\n HostName example.com\n Port 22\n IdentityFile none\n IdentityAgent none\n"
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: "box", TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			reply := code.Reply{Nonce: invite.Nonce, Account: "dev", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{prepareTestHostKey(7).Fingerprint()}}
			lease, err := BeginInviteAcceptance(t.Context(), "box", reply, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			source, err := resolveExplicitJoinedRouteV1(lease.Context(), lease, path)
			if err != nil {
				t.Fatal(err)
			}
			defer source.close()
			var actual []string
			for _, hop := range source.hops {
				actual = append(actual, hop.logical)
			}
			if !slices.Equal(actual, test.want) {
				t.Fatalf("native route order: %v; want %v", actual, test.want)
			}
			if test.name == "ssh-uri-prefix" && (source.hops[0].user != "alice" || source.hops[0].port != 2201) {
				t.Fatal("the native SSH URI user/port override was lost")
			}
		})
	}
}

func TestNativeJoinedRouteCycleAndCombinedBound(t *testing.T) {
	if _, err := exec.LookPath("/usr/bin/ssh"); err != nil {
		t.Skip("native OpenSSH is unavailable")
	}
	for _, config := range []string{
		"Host box\n ProxyJump inner\nHost inner\n ProxyJump outer\nHost outer\n ProxyJump inner\n",
		"Host box\n ProxyJump a,b,c,d,e,f,g,h\nHost a\n ProxyJump extra\n",
	} {
		t.Run(config[:8], func(t *testing.T) {
			inviteHome(t)
			home := routeReadsetHomeV1(t)
			t.Setenv("PATH", "/usr/bin:/bin")
			path := filepath.Join(home, "selected.conf")
			if err := os.WriteFile(path, []byte(config+"Host *\n User fixture\n HostName example.com\n IdentityFile none\n IdentityAgent none\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: "box", TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			reply := code.Reply{Nonce: invite.Nonce, Account: "dev", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{prepareTestHostKey(7).Fingerprint()}}
			lease, err := BeginInviteAcceptance(t.Context(), "box", reply, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			before := sshGRuns.Load()
			if source, err := resolveExplicitJoinedRouteV1(lease.Context(), lease, path); err == nil {
				source.close()
				t.Fatal("a cyclic or overlong nested route passed")
			}
			if sshGRuns.Load()-before > 10 {
				t.Fatal("native resolution exceeded the complete bounded route")
			}
		})
	}
}
