package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitEntryConfig(t *testing.T, h Host) (gitSSHConfig, *sshLogFile) {
	t.Helper()
	args, err := (&SSH{Host: h}).connArgs(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	log, err := newSSHLog()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.finish() })
	args = append(args, NoPortForwards()...)
	args = append(args, "-E", log.path)
	digest, err := gitSSHEntryDigest(h)
	if err != nil {
		t.Fatal(err)
	}
	return gitSSHConfig{Version: 2, Name: h.Name, Args: args, Target: h.SSH, Root: "/repo", EntryDigest: digest}, log
}

func TestGitSSHEntryDigestIncludesNormalisedHostAndPool(t *testing.T) {
	h := Host{Name: "box", SSH: "box.example"}
	digest := func(h Host) string {
		t.Helper()
		s, err := gitSSHEntryDigest(h)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	empty := h
	empty.Labels, empty.Secrets = map[string]string{}, []string{}
	if digest(h) != digest(empty) {
		t.Fatal("semantically empty fields changed the saved-entry digest")
	}
	changed := h
	changed.SSH = "other.example"
	if digest(h) == digest(changed) || digest(h) == digest(h.WithPool(false)) || digest(h.WithPool(false)) == digest(h.WithPool(true)) {
		t.Fatal("a changed endpoint or pool setting reused a saved-entry digest")
	}
}

func TestGitSSHExecutorChecksFreshTierBeforeItsActualStart(t *testing.T) {
	dir, h := sshEnvironmentHost(t, TierOwn)
	config, log := gitEntryConfig(t, h)
	if err := writeTiersFile(Tiers{Migrated: nowUTC(), entries: map[string]TierEntry{
		h.Name: {Name: h.Name, Tier: TierShared},
	}}); err != nil {
		t.Fatal(err)
	}
	more := []string{"-o", "SendEnv=GIT_PROTOCOL", h.SSH, "git-upload-pack '/repo'"}
	_, release, err := prepareGitSSHStart(t.Context(), config, log.dir, more)
	if release != nil {
		release()
	}
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "ssh_setenv" || ue.Kind != KindRefused {
		t.Fatalf("the executor reused an earlier own-tier decision: %v", err)
	}
	for _, call := range sshCalls(t, dir) {
		if !strings.Contains(call, " -G ") && !strings.Contains(call, " -O ") {
			t.Fatalf("the refused Git executor started SSH: %s", call)
		}
	}
	_, unlock, err := LockHost(t.Context(), h.Name, "after refused Git start")
	if err != nil {
		t.Fatalf("a refused Git start retained the host lock: %v", err)
	}
	unlock()
}

func TestGitSSHExecutorRejectsAChangedSavedEntry(t *testing.T) {
	_, h := sshEnvironmentHost(t, TierOwn)
	config, log := gitEntryConfig(t, h)
	changed := h
	changed.Labels = map[string]string{"changed": "yes"}
	if err := SaveHosts(context.Background(), []Host{changed}); err != nil {
		t.Fatal(err)
	}
	_, release, err := prepareGitSSHStart(t.Context(), config, log.dir, []string{h.SSH, "git-upload-pack '/repo'"})
	if release != nil {
		release()
	}
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "host_changed" {
		t.Fatalf("the executor reused a changed saved entry: %v", err)
	}
}

func TestGitSSHLocalRefusalPreservesKindAndDropsData(t *testing.T) {
	dir := t.TempDir()
	refused := &UserError{Msg: "the environment is refused", Hint: "Remove SetEnv, then run pull again.", Code: "ssh_setenv", Kind: KindRefused,
		Data: json.RawMessage(`{"value":"must-not-be-recorded"}`)}
	writeGitLocalError(dir, refused)
	path := filepath.Join(dir, "status.json")
	status, ok := readGitSSHStatus(path)
	if !ok || status.LocalError == nil || status.LocalError.Code != "ssh_setenv" || status.LocalError.Kind != KindRefused {
		t.Fatalf("the local refusal lost its kind: %+v, %v", status, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "must-not-be-recorded") {
		t.Fatalf("the refusal recorded a value: %s, %v", data, err)
	}
	status.LocalError.Kind = KindFailed
	data, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readGitSSHStatus(path); ok {
		t.Fatal("the status reader accepted a mismatched error kind")
	}
}
