package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sshEnvironmentHost(t *testing.T, tier Tier) (string, Host) {
	t.Helper()
	sshEnvDirs(t)
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: map[string]TierEntry{
		h.Name: {Name: h.Name, Tier: tier},
	}}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, h.Name)
	put(t, dir, "values", values("ask")+"setenv PRIVATE_MARKER=kept-private\r\n")
	return dir, h
}

func TestSSHEnvironmentGateCoversEveryAuthenticatedBuilder(t *testing.T) {
	dir, h := sshEnvironmentHost(t, TierShared)
	s := &SSH{Host: h}
	for name, run := range map[string]func() error{
		"command": func() error { _, err := s.Command(t.Context(), "true"); return err },
		"shell":   func() error { _, err := s.Shell(t.Context()); return err },
		"forward": func() error { _, err := s.Forward(t.Context(), nil); return err },
		"run":     func() error { _, err := s.Run(t.Context(), "true", nil); return err },
		"start":   func() error { _, err := s.Start(t.Context(), "true"); return err },
		"shell runner": func() error {
			return s.RunShell(t.Context(), nil, io.Discard, io.Discard)
		},
		"forward runner": func() error {
			return s.RunForward(t.Context(), nil, nil, nil, nil, nil)
		},
		"debug log": func() error {
			_, err := s.VerboseLog(t.Context())
			return err
		},
		"git fetch": func() error {
			_, err := RunGitFetch(t.Context(), h, "/repo", "main", "fetched", false)
			return err
		},
	} {
		err := run()
		var ue *UserError
		if !errors.As(err, &ue) || ue.Code != "ssh_setenv" || ue.Kind != KindRefused {
			t.Fatalf("%s did not refuse the effective environment: %v", name, err)
		}
		if strings.Contains(err.Error(), "PRIVATE_MARKER") || strings.Contains(err.Error(), "kept-private") {
			t.Fatalf("%s exposed an environment value: %v", name, err)
		}
	}
	for _, call := range sshCalls(t, dir) {
		if !strings.Contains(call, " -G ") && !strings.Contains(call, " -O ") {
			t.Fatalf("environment refusal allowed an authenticated SSH start: %s", call)
		}
	}
}

func TestSSHEnvironmentAllowsARecordedOwnHost(t *testing.T) {
	_, h := sshEnvironmentHost(t, TierOwn)
	s := &SSH{Host: h}
	for name, build := range map[string]func() error{
		"command": func() error { _, err := s.Command(t.Context(), "true"); return err },
		"shell":   func() error { _, err := s.Shell(t.Context()); return err },
		"forward": func() error { _, err := s.Forward(t.Context(), nil); return err },
	} {
		if err := build(); err != nil {
			t.Fatalf("%s refused an own host's environment: %v", name, err)
		}
	}
	log, err := newSSHLog()
	if err != nil {
		t.Fatal(err)
	}
	defer log.finish()
	args, err := s.connArgs(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, NoPortForwards()...)
	args = append(args, "-E", log.path)
	entryDigest, err := gitSSHEntryDigest(h)
	if err != nil {
		t.Fatal(err)
	}
	config := gitSSHConfig{Version: 2, Name: h.Name, Args: args, Target: h.SSH, Root: "/repo", EntryDigest: entryDigest}
	if err := validateGitSSHConfig(config, log.dir); err != nil {
		t.Fatalf("the private Git executor refused the own-host wrapper: %v", err)
	}
}

func TestSSHEnvironmentGateCoversAnOwnedUnsavedStage(t *testing.T) {
	sshEnvDirs(t)
	dir := fakeSSH(t)
	h := Host{Name: "pending", SSH: "pending.example"}
	ctx, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	if err := writePin(h.Name, currentStage(h.Name), []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "values", values("ask")+"setenv PRIVATE_MARKER=kept-private\n")
	_, err = (&SSH{Host: h}).Command(ctx, "true")
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "ssh_setenv" || ue.Kind != KindRefused {
		t.Fatalf("a staged unsaved connection bypassed the environment gate: %v", err)
	}
	if saved, err := savedHost(h.Name); err != nil || saved {
		t.Fatalf("environment checking published an unsaved host: %v, %v", saved, err)
	}
	for _, call := range sshCalls(t, dir) {
		if !strings.Contains(call, " -G ") && !strings.Contains(call, " -O ") {
			t.Fatalf("a staged environment refusal allowed SSH to start: %s", call)
		}
	}
}

func TestSSHEnvironmentPoliciesNeverShareAnOlderMaster(t *testing.T) {
	dir, h := sshEnvironmentHost(t, TierOwn)
	if !controlMaster {
		t.Skip("this platform does not use SSH masters")
	}
	s := &SSH{Host: h}
	own, err := s.connArgs(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	var legacy string
	if err := readPinLocked(t.Context(), h.Name, func() {
		keys, err := readPin(h.Name)
		if err != nil {
			t.Error(err)
			return
		}
		legacy = markControl(sharedDir(), h.Name, controlKey(h.Name, keys))
	}); err != nil || legacy == "" {
		t.Fatalf("could not create a pre-policy master epoch: %v", err)
	}
	if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: map[string]TierEntry{
		h.Name: {Name: h.Name, Tier: TierShared},
	}}); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "values", values("ask"))
	restricted, err := s.connArgs(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	ownEpoch := strings.ReplaceAll(filepath.Dir(expandedTestSocket(t, own)), "%%", "%")
	restrictedEpoch := strings.ReplaceAll(filepath.Dir(expandedTestSocket(t, restricted)), "%%", "%")
	if ownEpoch == restrictedEpoch || ownEpoch == legacy || restrictedEpoch == legacy {
		t.Fatalf("environment policies reused a master's retained options: own=%s restricted=%s old=%s", ownEpoch, restrictedEpoch, legacy)
	}
	if err := retireControls(t.Context(), h.Name); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ownEpoch, restrictedEpoch, legacy} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retirement left an environment-policy epoch: %s, %v", path, err)
		}
	}
}
