package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/join/code"
)

func inviteHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}
func TestInviteStoreKeyExpiryAndNonceOwnership(t *testing.T) {
	inviteHome(t)
	now := time.Now().UTC().Truncate(time.Second)
	inv, encoded, err := CreateHostInvite(context.Background(), InviteOptions{Name: "box", Labels: map[string]string{"place": "desk"}, TTL: time.Hour, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := code.DecodeInvite(encoded, now)
	if err != nil || parsed.PublicKey != inv.PublicKey || parsed.Nonce != inv.Nonce {
		t.Fatalf("invite %+v %v", parsed, err)
	}
	if st, err := os.Stat(inv.IdentityFile); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions: %v %v", st, err)
	}
	if _, _, err = CreateHostInvite(context.Background(), InviteOptions{Name: "box", TTL: time.Hour, Now: now}); err == nil {
		t.Fatal("replaced open invite")
	}
	r := code.Reply{Nonce: inv.Nonce, Account: "worker", Addresses: []string{"192.0.2.4"}, Port: 22, Fingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}
	lease, err := BeginInviteAcceptance(context.Background(), "box", r, now)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Context().Value(heldKey{"box"}) != nil {
		t.Fatal("invite donated a host OP into preparation")
	}
	if err = lease.Consume(); err == nil {
		t.Fatal("a read invite became a registration")
	}
	if err = RecordInviteRegistrationV1(lease.Context(), &HostPrepareTargetV1{}); err == nil {
		t.Fatal("a fabricated target became saved")
	}
	lease.Close()
	if HasPin("box") {
		t.Fatal("an unchecked reply pinned a key")
	}
	if _, err = os.Stat(invitePath("box")); err != nil {
		t.Fatal("failed acceptance consumed the invite")
	}
	r.Nonce++
	if _, err = BeginInviteAcceptance(context.Background(), "box", r, now); err == nil {
		t.Fatal("wrong nonce accepted")
	}
	r.Nonce = inv.Nonce
	_, err = BeginInviteAcceptance(context.Background(), "box", r, inv.Expires)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "invite_expired" {
		t.Fatalf("expiry: %v", err)
	}
}
func TestInviteInvalidFieldsDoNotCreateState(t *testing.T) {
	inviteHome(t)
	reply := code.Reply{Nonce: 1, Account: "worker\nroot", Addresses: []string{"192.0.2.1"}, Port: 22, Fingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}
	if _, err := BeginInviteAcceptance(context.Background(), "box", reply, time.Now()); err == nil {
		t.Fatal("accepted newline account")
	}
	if _, err := os.Stat(filepath.Join(StateDir(), "invites")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid reply changed invite state: %v", err)
	}
}
func TestInviteConfigRendersAllHopsAndRollsBack(t *testing.T) {
	inviteHome(t)
	fp := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	r := code.Reply{Nonce: 1, Account: "worker", Addresses: []string{"192.0.2.3"}, Port: 2222, Fingerprints: []string{fp}, Hops: []code.Hop{{Account: "gateway", Addresses: []string{"192.0.2.1"}, Port: 22, Fingerprints: []string{fp}}, {Account: "middle", Addresses: []string{"192.0.2.2"}, Port: 23, Fingerprints: []string{fp}}}}
	pins := []string{filepath.Join(ConfigDir(), "ssh", "hop1"), filepath.Join(ConfigDir(), "ssh", "hop2")}
	text, alias, err := JoinedSSHConfig("box", r.Addresses[0], "", r, pins)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ProxyJump " + alias + "-hop-1," + alias + "-hop-2", "KnownHostsCommand none", "StrictHostKeyChecking yes", "NoHostAuthenticationForLocalhost no"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	r.Hops = nil
	h, stage, err := StageInviteSSH(context.Background(), stagingInviteFixtureV1(t), r, r.Addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(h.SSHConfig); err != nil {
		t.Fatal(err)
	}
	if err = stage.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(h.SSHConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed add kept its generated SSH config")
	}
}
func TestInvitePinSetRejectsAmbiguityAndDuplicates(t *testing.T) {
	a := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	b := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE"
	for _, v := range []struct {
		want  string
		wants []string
	}{{a, []string{b}}, {a, []string{a, b}}, {"", []string{a, a}}, {"", []string{"bad"}}} {
		if err := checkFingerprintSet(v.want, v.wants); err == nil {
			t.Fatal("ambiguous pin set accepted")
		}
	}
	if err := checkFingerprintSet(a, []string{a}); err != nil {
		t.Fatal(err)
	}
}

func TestInviteStagePreservesByteIdenticalForeignReplacement(t *testing.T) {
	inviteHome(t)
	r := code.Reply{Nonce: 1, Account: "dev", Addresses: []string{"192.0.2.1"}, Port: 22, Fingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}
	h, stage, err := StageInviteSSH(context.Background(), stagingInviteFixtureV1(t), r, r.Addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(h.SSHConfig)
	if err != nil {
		t.Fatal(err)
	}
	replacement := h.SSHConfig + ".replacement"
	if err = os.WriteFile(replacement, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, h.SSHConfig); err != nil {
		t.Fatal(err)
	}
	if err = stage.Close(); err == nil {
		t.Fatal("identical bytes donated native cleanup ownership")
	}
	if got, err := os.ReadFile(h.SSHConfig); err != nil || string(got) != string(body) {
		t.Fatalf("foreign replacement was removed: %v", err)
	}
}

func TestInviteOwnershipPairRejectsEditReplacementAndExtraLink(t *testing.T) {
	for _, change := range []string{"edit", "replacement", "extra-link"} {
		t.Run(change, func(t *testing.T) {
			inviteHome(t)
			r := code.Reply{Nonce: 1, Account: "dev", Addresses: []string{"192.0.2.1"}, Port: 22, Fingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}
			h, stage, err := StageInviteSSH(context.Background(), stagingInviteFixtureV1(t), r, r.Addresses[0])
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if stage.ownership != nil {
					stage.ownership.close()
				}
			})
			if h.JoinedSSH == nil || !validJoinedRecord(h.JoinedSSH) {
				t.Fatal("original writer emitted no ownership data")
			}
			if err = stage.ownership.checkCurrent(); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "edit":
				err = os.WriteFile(h.SSHConfig, []byte("changed\n"), 0o600)
			case "replacement":
				body, e := os.ReadFile(h.SSHConfig)
				if e != nil {
					t.Fatal(e)
				}
				temp := h.SSHConfig + ".new"
				if err = os.WriteFile(temp, body, 0o600); err == nil {
					err = os.Rename(temp, h.SSHConfig)
				}
			case "extra-link":
				err = os.Link(h.SSHConfig, h.SSHConfig+".extra")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = stage.ownership.checkCurrent(); err == nil {
				t.Fatal("foreign shape became original ownership")
			}
			if err = stage.Close(); err == nil {
				t.Fatal("uncertain abort claimed success")
			}
			if _, err = os.Lstat(h.SSHConfig); err != nil {
				t.Fatal("foreign/changed leaf was removed")
			}
		})
	}
}
func TestJoinedOwnershipDataCannotBootstrapOrDropAuthority(t *testing.T) {
	record := &JoinedSSHOwnership{Version: 1, Commit: strings.Repeat("A", 26), ManifestSHA256: strings.Repeat("a", 64), ManifestNative: "unix:1:2"}
	h := Host{Name: "box", SSH: "joined-box", JoinedSSH: record}
	if err := checkJoinedIntroductionLocked(context.Background(), HostChange{Now: &h}); err == nil {
		t.Fatal("metadata-only joined owner was introduced")
	}
	next := h
	next.JoinedSSH = nil
	if err := checkJoinedIntroductionLocked(context.Background(), HostChange{Was: &h, Now: &next}); err == nil {
		t.Fatal("original owner was dropped")
	}
	if err := checkJoinedBulkRows(nil, []Host{h}); err == nil {
		t.Fatal("bulk metadata introduced native ownership")
	}
	if err := checkJoinedBulkRows([]Host{h}, nil); err == nil {
		t.Fatal("bulk metadata dropped native ownership")
	}
	if err := checkJoinedBulkRows([]Host{h}, []Host{h}); err != nil {
		t.Fatal(err)
	}
	clone := cloneGrantHost(h)
	clone.JoinedSSH.Commit = "changed"
	if h.JoinedSSH.Commit == "changed" {
		t.Fatal("saved ownership reference was aliased through a metadata clone")
	}
}

func TestJoinedOwnershipDataCannotRedirectItsOriginalRoute(t *testing.T) {
	record := &JoinedSSHOwnership{Version: 1, Commit: strings.Repeat("A", 26), ManifestSHA256: strings.Repeat("a", 64), ManifestNative: "unix:1:2"}
	original := Host{Name: "box", SSH: "joined-box", SSHConfig: "owned-config", JoinedSSH: record}
	changed := cloneGrantHost(original)
	changed.SSHConfig = "foreign-config"
	if err := checkJoinedIntroductionLocked(context.Background(), HostChange{Was: &original, Now: &changed}); err == nil {
		t.Fatal("unchanged ownership data redirected a managed route")
	}
	if err := checkJoinedBulkRows([]Host{original}, []Host{changed}); err == nil {
		t.Fatal("bulk data redirected a managed route")
	}
}

func TestInviteStorageKeepsCaseDistinctNamesAndMaximumLength(t *testing.T) {
	inviteHome(t)
	var records []HostInvite
	for _, name := range []string{"Box", "box", strings.Repeat("a", 63)} {
		record, _, err := CreateHostInvite(context.Background(), InviteOptions{Name: name, TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := readHostInvite(name)
		if err != nil || stored.Name != name {
			t.Fatalf("exact display name was lost: %q %v", name, err)
		}
		records = append(records, record)
		if len(filepath.Base(record.IdentityFile)) > 255 || len(filepath.Base(invitePath(name))) > 255 {
			t.Fatal("long host name exceeded one filesystem leaf")
		}
	}
	if strings.EqualFold(invitePath("Box"), invitePath("box")) || strings.EqualFold(records[0].IdentityFile, records[1].IdentityFile) || records[0].PublicKey == records[1].PublicKey {
		t.Fatal("case-distinct names shared an invite or host key")
	}
	a, err := os.Stat(records[0].IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(records[1].IdentityFile)
	if err != nil || os.SameFile(a, b) {
		t.Fatal("case-distinct keys shared an actual inode")
	}
}
func TestInviteBadNamesHaveNoFileEffects(t *testing.T) {
	for _, name := range []string{"", "auto", "../escape", "box/child", "box.", "-box", "雪", strings.Repeat("a", 64), "bad\nname"} {
		t.Run("name", func(t *testing.T) {
			inviteHome(t)
			if ValidHostName(name) {
				t.Fatalf("test name unexpectedly belongs to the existing grammar: %q", name)
			}
			if _, _, err := CreateHostInvite(context.Background(), InviteOptions{Name: name, TTL: time.Hour}); err == nil {
				t.Fatal("bad name created an invite")
			}
			for _, dir := range []string{filepath.Join(StateDir(), "invites"), filepath.Join(ConfigDir(), "ssh")} {
				if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("malformed name caused a file effect: %v", err)
				}
			}
		})
	}
}

func TestFailedInviteKeyCleanupRetainsByteIdenticalReplacement(t *testing.T) {
	inviteHome(t)
	path := filepath.Join(ConfigDir(), "ssh", "id-creator-test")
	_, birth, err := inviteIdentity(path)
	if err != nil || birth == nil {
		t.Fatalf("key creator: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := path + ".new"
	if err = os.WriteFile(replacement, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err = birth.remove(); err == nil {
		t.Fatal("a matching byte sequence donated private-key cleanup ownership")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("foreign replacement was removed")
	}
}

func stagingInviteFixtureV1(t *testing.T) HostInvite {
	t.Helper()
	invite, _, err := CreateHostInvite(t.Context(), InviteOptions{Name: "box", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return invite
}
