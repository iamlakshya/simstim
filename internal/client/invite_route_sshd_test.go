//go:build !windows

package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/join/code"
)

// The existing PIN_SSHD lane owns the temporary loopback daemon. This exercises
// real OpenSSH's existing trust decision, including the negative key case.
func TestRealSSHDJoinedBastionRequiresOriginalUserTrust(t *testing.T) {
	directory := sshdHome(t)
	server := realSSHD(t, directory, "bastion-key")
	home := routeReadsetHomeV1(t)
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint("wrong-key-", wrong), func(t *testing.T) {
			name := "trusted"
			if wrong {
				name = "wrong"
			}
			known := filepath.Join(home, name+".keys")
			key := pubKey(t, server.key)
			if wrong {
				key = prepareTestHostKey(9).line("")[1:]
			}
			if err := os.WriteFile(known, []byte("original-bastion "+key+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			selected := filepath.Join(home, name+".conf")
			body := fmt.Sprintf("Host %s\n ProxyJump jump\nHost jump\n HostName 127.0.0.1\n Port %d\n User nobody\n IdentityFile none\n IdentityAgent none\n UserKnownHostsFile %q\n GlobalKnownHostsFile /dev/null\n HostKeyAlias original-bastion\n StrictHostKeyChecking yes\n", name, server.port, known)
			if err := os.WriteFile(selected, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: name, TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			reply := code.Reply{Nonce: invite.Nonce, Account: "dev", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{prepareTestHostKey(7).Fingerprint()}}
			lease, err := BeginInviteAcceptance(t.Context(), name, reply, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			source, err := resolveExplicitJoinedRouteV1(lease.Context(), lease, selected)
			if err != nil {
				t.Fatal(err)
			}
			defer source.close()
			projection, err := captureExplicitBastionsV1(lease.Context(), source)
			if wrong {
				if err == nil {
					projection.close()
					t.Fatal("a wrong original bastion key was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer projection.close()
			if len(projection.bastions) != 1 || len(projection.bastions[0].keys) != 1 {
				t.Fatal("the actual bastion trust/key capture was not retained")
			}
			if err := os.WriteFile(selected, []byte(body+"# changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if projection.check(context.Background()) == nil {
				t.Fatal("changed explicit source remained usable after actual trust")
			}
		})
	}
}
