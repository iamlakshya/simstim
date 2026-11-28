package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestNewAndResumeCPUsAndMemoryFlags(t *testing.T) {
	newTestHost(t)

	// --memory and --memory-max together fail with exit code 2.
	if got, code := lineErr(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--memory", "1G", "--memory-max", "2G", "--", "sleep", "60"); code != 2 || !strings.Contains(got, "cannot go together") {
		t.Fatalf("--memory + --memory-max = %d:\n%s", code, got)
	}
	if got, code := lineErr(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--cpus", "0", "--", "sleep", "60"); code != 2 || !strings.Contains(got, "not a positive whole number") {
		t.Fatalf("--cpus 0 = %d:\n%s", code, got)
	}

	// Start with --cpus 2 --memory 512M, then resume with --cpus 4 --memory 1G.
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "sized", "--cpus", "2", "--memory", "512M", "--", "/bin/sh", "-c", "sleep 2; exit 0"))
	if m == nil {
		t.Fatal("new started no session")
	}
	id := m[1]
	manifest, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Limits.CPUs != 2 || manifest.Limits.MemoryMax != capFor(512<<20) {
		t.Fatalf("initial limits = %+v", manifest.Limits)
	}

	// resume --memory 1G on a running session changes nothing and says so on stderr.
	got := okSaid(t, "resume", "box/"+id, "--detach", "--memory", "1G")
	if !strings.Contains(got, "--memory changes nothing") {
		t.Fatalf("resume running with --memory =\n%s", got)
	}
	if after, _ := host.LoadManifest(id); after.Limits.MemoryMax != capFor(512<<20) {
		t.Fatalf("running session memory changed to %d", after.Limits.MemoryMax)
	}

	// Wait for exit, then resume with --cpus 4 --memory 1G.
	agentEnded(t, id)
	okSaid(t, "resume", "box/"+id, "--detach", "--cpus", "4", "--memory", "1G")
	resumed, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Limits.CPUs != 4 || resumed.Limits.MemoryMax != capFor(1<<30) {
		t.Fatalf("resumed limits = %+v, want cpus=4 memory=%d", resumed.Limits, capFor(1<<30))
	}
}

func TestNewEnvAndIdentityVarsEndToEnd(t *testing.T) {
	th := newTestHost(t)
	writeSettings(t, "[secrets]\nAPI_TOKEN = \"echo tok\"\n\n[sandbox.env]\nFROM_CFG = \"cfg\"\n")
	t.Setenv("LAPTOP_VAR", "from-laptop")

	// Secret name refused with env_is_secret (exit 5).
	if got, code := lineErr(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--env", "API_TOKEN=raw", "--", "sleep", "5"); code != 5 || !strings.Contains(got, "configured under [secrets]") {
		t.Fatalf("env_is_secret = %d:\n%s", code, got)
	}
	// Missing laptop variable refused with exit 2.
	if _, code := lineErr(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--env", "MISSING_LAPTOP_VAR_XYZ", "--", "sleep", "5"); code != 2 {
		t.Fatalf("missing laptop var exited %d, want 2", code)
	}

	envFile := filepath.Join(t.TempDir(), "vars.env")
	if err := os.WriteFile(envFile, []byte("# comment\nFROM_FILE=file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(th.h.Home, "env-out.txt")
	cmd := "printf '%s|%s|%s|%s|%s|%s|%s\\n' \"$FROM_CFG\" \"$FROM_FILE\" \"$LAPTOP_VAR\" \"$OVERRIDE\" \"$" +
		brand.Env("HOST") + "\" \"$" + brand.Env("AGENT") + "\" \"$" + brand.Env("SANDBOX") + "\" > '" + outFile + "'"
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "envcheck",
		"--env-file", envFile, "--env", "LAPTOP_VAR", "--env", "OVERRIDE=one", "--env", "OVERRIDE=two",
		"--", "/bin/sh", "-c", cmd))
	if m == nil {
		t.Fatal("new started no session")
	}
	hosttest.WaitFor(t, 5*time.Second, "env output file", func() bool {
		b, err := os.ReadFile(outFile)
		return err == nil && len(b) > 0
	})
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "cfg|file-value|from-laptop|two|box|shell|envcheck\n"
	if string(got) != want {
		t.Fatalf("session env output = %q, want %q", string(got), want)
	}
}

func TestNewCopyInSensitiveRefusalAndUpload(t *testing.T) {
	th := newTestHost(t)
	sshDir := filepath.Join(th.h.Home, ".ssh")
	_ = os.MkdirAll(sshDir, 0o700)
	_ = os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("PRIVATE"), 0o600)

	// Refused before touching host with exit 5 (copy_sensitive).
	if got, code := lineErr(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--copy", "~/.ssh/id_ed25519", "--", "sleep", "5"); code != 5 || !strings.Contains(got, "private folder") {
		t.Fatalf("copy_sensitive = %d:\n%s", code, got)
	}

	// Copy a safe file into ~/copied-skill.txt and verify the session reads it immediately.
	src := filepath.Join(t.TempDir(), "skill.txt")
	if err := os.WriteFile(src, []byte("from-laptop-copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	dstCheck := filepath.Join(th.h.Home, "verified-copy.txt")
	okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "copier",
		"--copy", src+":~/copied-skill.txt",
		"--", "/bin/sh", "-c", "cp ~/copied-skill.txt '"+dstCheck+"' && sleep 30")
	hosttest.WaitFor(t, 5*time.Second, "copied file read by session", func() bool {
		b, err := os.ReadFile(dstCheck)
		return err == nil && string(b) == "from-laptop-copy"
	})
}

func initTestRepo(t *testing.T, dir, branch, file, content, origin string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Seed Tester",
			"GIT_AUTHOR_EMAIL=seed@example.com",
			"GIT_COMMITTER_NAME=Seed Tester",
			"GIT_COMMITTER_EMAIL=seed@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", branch)
	run("config", "user.name", "Seed Tester")
	run("config", "user.email", "seed@example.com")
	if origin != "" {
		run("remote", "add", "origin", origin)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
}

func TestNewSeedDirtyAndMultiRepoPull(t *testing.T) {
	th := newTestHost(t)
	repoBase := t.TempDir()
	apiRepo := filepath.Join(repoBase, "api")
	webRepo := filepath.Join(repoBase, "web")
	initTestRepo(t, apiRepo, "main", "api.txt", "v1\n", "https://example.invalid/org/api.git")
	initTestRepo(t, webRepo, "main", "web.txt", "w1\n", "https://example.invalid/org/web.git")

	// Dirty edit + untracked file in apiRepo.
	if err := os.WriteFile(filepath.Join(apiRepo, "api.txt"), []byte("v2-dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiRepo, "extra.txt"), []byte("new-untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	probeOut := filepath.Join(th.h.Home, "seed-probe.txt")
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--seed", apiRepo, "--seed-dirty",
		"--", "/bin/sh", "-c", "printf '%s|%s|%s|%s\\n' \"$(cat api.txt)\" \"$(cat extra.txt)\" \"$(git config user.email)\" \"$(git remote get-url origin)\" > '"+probeOut+"' && sleep 30"))
	if m == nil {
		t.Fatal("new --seed started no session")
	}
	hosttest.WaitFor(t, 5*time.Second, "seed probe output", func() bool {
		b, err := os.ReadFile(probeOut)
		return err == nil && len(b) > 0
	})
	got, _ := os.ReadFile(probeOut)
	if want := "v2-dirty|new-untracked|seed@example.com|https://example.invalid/org/api.git\n"; string(got) != want {
		t.Fatalf("seeded session probe = %q, want %q", string(got), want)
	}

	// Multi-repo seed: pull without --repo fails exit 2 listing both repos; pull --repo web succeeds.
	m2 := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "duo",
		"--seed", apiRepo, "--seed", webRepo,
		"--", "/bin/sh", "-c", "cd web && echo w2 >> web.txt && git add web.txt && git commit -m 'update web' && sleep 30"))
	if m2 == nil {
		t.Fatal("multi-repo new started no session")
	}
	duoID := m2[1]
	duoManifest, err := host.LoadManifest(duoID)
	if err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, "commit inside seeded web clone", func() bool {
		out, err := exec.Command("git", "-C", duoManifest.SeedRepos[1].Dir, "rev-list", "--count", "HEAD").Output()
		return err == nil && strings.TrimSpace(string(out)) == "2"
	})

	origWD, _ := os.Getwd()
	if err := os.Chdir(webRepo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	if errMsg, code := lineErr(t, "pull", "box/"+duoID, "--into", "pulled-web"); code != 2 || !strings.Contains(errMsg, "api, web") {
		t.Fatalf("pull without --repo = %d:\n%s", code, errMsg)
	}
	okSaid(t, "pull", "box/"+duoID, "--repo", "web", "--into", "pulled-web")
	out, err := exec.Command("git", "-C", webRepo, "show", "pulled-web:web.txt").Output()
	if err != nil || !strings.Contains(string(out), "w2") {
		t.Fatalf("pulled-web:web.txt = %q, err=%v", out, err)
	}
}

func TestConfigCheckSandbox(t *testing.T) {
	newTestHost(t)
	writeSettings(t, `
[sandbox]
cpus = 2
memory = "4G"
disk_max = "10G"
copy_max = "64M"
seed_max = "512M"
copy_in = ["~/.claude/skills"]

[sandbox.env]
LOG_LEVEL = "debug"
`)
	ok(t, "config", "check")

	writeSettings(t, `
[secrets]
API_KEY = "echo k"

[sandbox]
cpus = -1
memory = "lots"
copy_in = ["/tmp/a:relative/path"]

[sandbox.env]
API_KEY = "bad"
`)
	out, err := run(t, "config", "check")
	var se silentError
	if !errors.As(err, &se) || se.code != 1 {
		t.Fatalf("invalid sandbox config err = %v (out=%q), want silentError{1}", err, out)
	}
	for _, want := range []string{"sandbox.cpus", "sandbox.memory", "sandbox.copy_in", "in [secrets]"} {
		if !strings.Contains(out, want) {
			t.Errorf("config check output missing %q:\n%s", want, out)
		}
	}
}

func TestNewCopyInWithSecret(t *testing.T) {
	th := newTestHost(t)
	writeSettings(t, "[secrets]\nDEFERRED_SECRET = \"echo secret-over-deferred\"\n")

	src := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(src, []byte("copy-and-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(th.h.Home, "copy-secret-out.txt")
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "copysec",
		"--copy", src+":~/note-sec.txt", "--secret", "DEFERRED_SECRET",
		"--", "/bin/sh", "-c", "printf '%s|%s\\n' \"$(cat ~/note-sec.txt)\" \"$DEFERRED_SECRET\" > '"+probe+"' && sleep 30"))
	if m == nil {
		t.Fatal("new --copy --secret started no session")
	}
	id := m[1]
	hosttest.WaitFor(t, 5*time.Second, "copy + secret output", func() bool {
		b, err := os.ReadFile(probe)
		return err == nil && len(b) > 0
	})
	got, _ := os.ReadFile(probe)
	if want := "copy-and-secret|secret-over-deferred\n"; string(got) != want {
		t.Fatalf("session output = %q, want %q", string(got), want)
	}
	manifest, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Secrets) != 1 || manifest.Secrets[0] != "DEFERRED_SECRET" {
		t.Fatalf("manifest.Secrets = %v, want [DEFERRED_SECRET]", manifest.Secrets)
	}
}

func TestRestoreAndAttachResendsPendingCLICopyIn(t *testing.T) {
	th := newTestHost(t)
	src := filepath.Join(t.TempDir(), "cli-only.txt")
	if err := os.WriteFile(src, []byte("from-cli-copy-flag"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(th.h.Home, "cli-restored.txt")
	m := startedRE.FindStringSubmatch(okSaid(t, "new", "box", "--detach", "--wall", "none", "--downgrade", "--name", "pendingcli",
		"--copy", src+":~/cli-restored.txt",
		"--", "sleep", "60"))
	if m == nil {
		t.Fatal("new --copy started no session")
	}
	id := m[1]

	// Simulate a boot-time restore without the laptop connected: the copied file
	// is missing and the manifest marks CopiesPending=true, while config.toml has
	// no copy_in entry.
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target before simulated restore: %v", err)
	}
	manifest, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	manifest.CopiesPending = true
	if err := manifest.Save(); err != nil {
		t.Fatal(err)
	}

	lsOut := ok(t, "ls", "box")
	if !strings.Contains(lsOut, "(copies not sent)") {
		t.Fatalf("ls output missing (copies not sent):\n%s", lsOut)
	}
	rawJSON := okJSON(t, "ls", "box", "--json")
	var groups []hostSessions
	if err := json.Unmarshal([]byte(rawJSON), &groups); err != nil {
		t.Fatalf("decode ls --json: %v", err)
	}
	var sess *client.Session
	for _, g := range groups {
		for i := range g.Sessions {
			if g.Sessions[i].ID == id {
				s := g.Sessions[i]
				sess = &s
				break
			}
		}
	}
	if strings.Contains(rawJSON, `"copy_in"`) {
		t.Fatalf("ls --json must not expose host copy_in paths:\n%s", rawJSON)
	}
	if sess == nil || !sess.CopiesPending {
		t.Fatalf("ls --json session = %+v (raw=%s), want CopiesPending=true", sess, rawJSON)
	}
	if saved := client.SavedCopyIn("box", id); len(saved) != 1 {
		t.Fatalf("client.SavedCopyIn(box, %s) = %v, want 1 laptop-saved spec", id, saved)
	}

	// Next attach calls resendPendingCopies, which must re-upload the --copy CLI
	// file from laptop-saved state and clear CopiesPending.
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, h, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := resendPendingCopies(ctx, conn, h, sess); err != nil {
		t.Fatalf("resendPendingCopies: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "from-cli-copy-flag" {
		t.Fatalf("re-uploaded target = %q, err=%v", string(got), err)
	}
	after, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.CopiesPending {
		t.Fatal("CopiesPending stayed true after resendPendingCopies")
	}

	// Untrusted host test (G10): even if the host manifest points CopyIn at a
	// different laptop file and marks CopiesPending=true, another laptop without
	// that record in its own config/state must NOT upload the host-named path
	// and must leave CopiesPending=true.
	hostChosen := filepath.Join(t.TempDir(), "host-chosen.txt")
	if err := os.WriteFile(hostChosen, []byte("never-upload-from-host-path"), 0o600); err != nil {
		t.Fatal(err)
	}
	exfilTarget := filepath.Join(th.h.Home, "host-exfil.txt")
	after.CopyIn = []api.CopyInSpec{{Source: hostChosen, Guest: "/home/agent/host-exfil.txt"}}
	after.CopiesPending = true
	if err := after.Save(); err != nil {
		t.Fatal(err)
	}
	client.ForgetCopyIn("box", id)
	sess.CopiesPending = true
	if err := resendPendingCopies(ctx, conn, h, sess); err != nil {
		t.Fatalf("resendPendingCopies without laptop record: %v", err)
	}
	if _, err := os.Stat(exfilTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resendPendingCopies uploaded host-supplied path %s (stat err=%v)", exfilTarget, err)
	}
	stillPending, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if !stillPending.CopiesPending {
		t.Fatal("CopiesPending was cleared when no laptop-owned copy specs existed")
	}

	// Deleting the session forgets its laptop-saved copy-in record.
	if err := client.RememberCopyIn("box", id, []string{src + ":~/cli-restored.txt"}); err != nil {
		t.Fatal(err)
	}
	okSaid(t, "rm", "box/"+id, "-y")
	if saved := client.SavedCopyIn("box", id); len(saved) != 0 {
		t.Fatalf("SavedCopyIn after rm = %v, want empty", saved)
	}
}
