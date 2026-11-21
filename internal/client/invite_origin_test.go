package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/join/code"
)

func TestOriginalInviteRejectsChangedReplyBeforeProjection(t *testing.T) {
	for _, change := range []string{"nonce", "account", "address", "key", "port", "suggestion", "hop"} {
		t.Run(change, func(t *testing.T) {
			inviteHome(t)
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
			switch change {
			case "nonce":
				reply.Nonce++
			case "account":
				reply.Account = "other"
			case "address":
				reply.Addresses[0] = "192.0.2.5"
			case "key":
				reply.Fingerprints[0] = prepareTestHostKey(9).Fingerprint()
			case "port":
				reply.Port = 23
			case "suggestion":
				reply.SuggestedTier = "shared"
			case "hop":
				reply.Hops = []code.Hop{{Account: "jump", Addresses: []string{"192.0.2.6"}, Port: 22, Fingerprints: []string{prepareTestHostKey(9).Fingerprint()}}}
			}
			before := sshGRuns.Load()
			if _, _, err := lease.StageSSHV1(lease.Context(), reply, "192.0.2.4"); err == nil {
				t.Fatal("changed reply produced a default native projection")
			}
			if _, _, err := lease.StageSSHWithExplicitConfigV1(lease.Context(), reply, "192.0.2.4", filepath.Join(t.TempDir(), "unused")); err == nil {
				t.Fatal("changed reply produced an explicit native projection")
			}
			if sshGRuns.Load() != before {
				t.Fatal("changed reply reached native route resolution")
			}
			if lease.reply.Addresses[0] != "192.0.2.4" || lease.reply.Fingerprints[0] != prepareTestHostKey(7).Fingerprint() {
				t.Fatal("caller slice mutation changed the retained original reply")
			}
		})
	}
}

func TestOriginalInviteRejectsBorrowedStageAndChangedReturnedHost(t *testing.T) {
	inviteHome(t)
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
	borrowedHost, borrowed, err := StageInviteSSH(t.Context(), invite, reply, reply.Addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	defer borrowed.Close()
	if lease.BindSSH(borrowedHost, borrowed) == nil {
		t.Fatal("a raw staged Host donated original acceptance custody")
	}
	if _, err := lease.RegistrationContextV1(); err == nil {
		t.Fatal("a borrowed local stage manufactured a registration capture")
	}
	host, stage, err := lease.StageSSHV1(lease.Context(), reply, reply.Addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	changed := cloneObservationHost(host)
	changed.SSH = "other-target"
	if lease.BindSSH(changed, stage) == nil {
		t.Fatal("a changed returned Host redirected the native stage")
	}
	if err := lease.BindSSH(host, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.RegistrationContextV1(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lease.StageSSHV1(lease.Context(), reply, reply.Addresses[0]); err == nil {
		t.Fatal("one original acceptance issued another native stage")
	}
}

func TestOriginalInviteCancellationCannotBeDetachedFromRoute(t *testing.T) {
	inviteHome(t)
	invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: "box", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	reply := code.Reply{Nonce: invite.Nonce, Account: "dev", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{prepareTestHostKey(7).Fingerprint()}}
	ctx, cancel := context.WithCancel(t.Context())
	lease, err := BeginInviteAcceptance(ctx, "box", reply, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	cancel()
	if _, _, err := lease.StageSSHV1(context.WithoutCancel(lease.Context()), reply, reply.Addresses[0]); !errors.Is(err, context.Canceled) {
		t.Fatal("a new caller context renewed the cancelled original invite")
	}
}
