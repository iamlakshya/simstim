package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/join/code"
)

func TestFrozenJoinedRouteNeverRereadsChangedInclude(t *testing.T) {
	home := routeReadsetHomeV1(t)
	part := filepath.Join(home, "part.conf")
	root := filepath.Join(home, "selected.conf")
	if err := os.WriteFile(part, []byte("Host target\n ProxyJump bastion\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("Include "+part+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readset, err := captureExplicitRouteReadsetV1(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer readset.close()
	snapshot, err := freezeRouteConfigV1(t.Context(), readset)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	if err := os.WriteFile(part, []byte("Match exec must-never-be-resolved\n ProxyCommand other-command\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readset.check(t.Context()) == nil {
		t.Fatal("edited original Include remained current")
	}
	for _, file := range snapshot.files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), part) || strings.Contains(string(data), "must-never-be-resolved") || strings.Contains(string(data), "other-command") {
			t.Fatal("resolver snapshot contains a mutable original Include or later executable bytes")
		}
	}
	if err := snapshot.check(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestJoinedRouteReadsetRejectsLateAmbientRoot(t *testing.T) {
	home := routeReadsetHomeV1(t)
	selected := filepath.Join(home, "selected.conf")
	if err := os.WriteFile(selected, []byte("Host target\n ProxyJump bastion\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := captureExplicitRouteReadsetV1(t.Context(), selected)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host *\n IdentityFile /late-operator-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if source.check(t.Context()) == nil {
		t.Fatal("late ambient source was adopted")
	}
}

func TestJoinedRouteRecordRetainsNativeReadset(t *testing.T) {
	home := routeReadsetHomeV1(t)
	selected := filepath.Join(home, "selected.conf")
	if err := os.WriteFile(selected, []byte("Host target\n ProxyJump bastion\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readset, err := captureExplicitRouteReadsetV1(t.Context(), selected)
	if err != nil {
		t.Fatal(err)
	}
	defer readset.close()
	snapshot, err := freezeRouteConfigV1(t.Context(), readset)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	directory := t.TempDir()
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	p := &explicitRouteProjectionV1{source: &explicitRouteSourceV1{readset: readset, snapshot: snapshot}, directory: directory, directoryInfo: info, bastions: []explicitBastionV1{{}}}
	record, err := p.record()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded joinedRouteRecordV1
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	restriction, err := openJoinedRouteRestrictionV1(t.Context(), &decoded)
	if err != nil {
		t.Fatal(err)
	}
	defer restriction.close()
	replacement := selected + ".new"
	if err = os.WriteFile(replacement, []byte("Host target\n ProxyJump bastion\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, selected); err != nil {
		t.Fatal(err)
	}
	if restriction.check(t.Context()) == nil {
		t.Fatal("identical replacement became original saved route custody")
	}
}

func TestJoinedPublicKeyInviteSelectsOnlyOfferedAgentKey(t *testing.T) {
	inviteHome(t)
	public := prepareTestHostKey(4)
	publicFile := filepath.Join(t.TempDir(), "offered.pub")
	if err := os.WriteFile(publicFile, []byte(strings.TrimSpace(public.line(""))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: "box", TTL: time.Hour, PublicKeyFile: publicFile})
	if err != nil {
		t.Fatal(err)
	}
	reply := code.Reply{Nonce: invite.Nonce, Account: "dev", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{public.Fingerprint()}}
	host, stage, err := StageInviteSSH(t.Context(), invite, reply, reply.Addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	text, err := os.ReadFile(host.SSHConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "identity.pub") || !strings.Contains(string(text), "IdentitiesOnly yes") || strings.Contains(string(text), publicFile) {
		t.Fatal("public-key invite inherited another credential source")
	}
	origin, err := openJoinedSSHOriginV1(host, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer origin.close()
	if !origin.manifest.IdentityPublic || len(origin.manifest.Files) != 2 {
		t.Fatal("offered public key lacks original native owned-pair custody")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(host.SSHConfig), "identity.pub"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if origin.checkRetained() == nil {
		t.Fatal("edited offered identity remained the original writer's public key")
	}
}

func TestUnissuedRouteCannotReachProjection(t *testing.T) {
	if _, _, err := new(InviteAcceptance).StageSSHWithExplicitConfigV1(context.Background(), code.Reply{}, "", "/caller"); err == nil {
		t.Fatal("zero invitation produced a route")
	}
	if _, err := (&joinedProjectionSourceV1{}).OpenSSHNativeSource(t.Context()); err == nil {
		t.Fatal("zero source produced a closed projection")
	}
}

func TestInviteRouteRejectsChangedPresentationBeforeEffects(t *testing.T) {
	for _, change := range []string{"name", "nonce", "key", "expiry", "labels", "context", "closed"} {
		t.Run(change, func(t *testing.T) {
			inviteHome(t)
			invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: "box", Labels: map[string]string{"place": "original"}, TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			reply := code.Reply{Nonce: invite.Nonce, Account: "dev", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{prepareTestHostKey(7).Fingerprint()}}
			lease, err := BeginInviteAcceptance(t.Context(), "box", reply, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			ctx := lease.Context()
			switch change {
			case "name":
				lease.Invite.Name = "other"
			case "nonce":
				lease.Invite.Nonce++
			case "key":
				lease.Invite.PublicKey = "changed"
			case "expiry":
				lease.Invite.Expires = lease.Invite.Expires.Add(time.Hour)
			case "labels":
				lease.Invite.Labels["place"] = "changed"
				if lease.original.Labels["place"] != "original" {
					t.Fatal("presentation shared the private original label map")
				}
			case "context":
				ctx = context.Background()
			case "closed":
				lease.Close()
			}
			before := sshGRuns.Load()
			if _, _, err := lease.StageSSHWithExplicitConfigV1(ctx, reply, reply.Addresses[0], filepath.Join(t.TempDir(), "absent.conf")); err == nil {
				t.Fatal("changed original invite produced a route")
			}
			if sshGRuns.Load() != before {
				t.Fatal("a refused original invite reached native resolution")
			}
			matches, err := filepath.Glob(filepath.Join(ConfigDir(), "ssh", "joined-*"))
			if err != nil || len(matches) != 0 {
				t.Fatal("a refused invite wrote a route projection")
			}
		})
	}
}
func TestInviteRouteRechecksActualExpiryAndCurrentRecord(t *testing.T) {
	for _, change := range []string{"expired", "record"} {
		t.Run(change, func(t *testing.T) {
			inviteHome(t)
			now := time.Now()
			if change == "expired" {
				now = now.Add(-2 * time.Hour)
			}
			invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: "box", TTL: time.Hour, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			reply := code.Reply{Nonce: invite.Nonce, Account: "dev", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{prepareTestHostKey(7).Fingerprint()}}
			lease, err := BeginInviteAcceptance(t.Context(), "box", reply, now)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if change == "record" {
				changed := cloneHostInviteV1(invite)
				changed.PublicKey = strings.TrimSpace(prepareTestHostKey(9).line(""))
				data, err := json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(invitePath("box"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := lease.StageSSHWithExplicitConfigV1(lease.Context(), reply, reply.Addresses[0], "/unused-selected-source"); err == nil {
				t.Fatal("stale original invite reached route resolution")
			}
		})
	}
}
