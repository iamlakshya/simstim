package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// agentEnded waits until ls says that the agent of the session id ended.
func agentEnded(t *testing.T, id string) {
	t.Helper()
	ended := regexp.MustCompile(id + `\s.*(exited|errored)`)
	hosttest.WaitFor(t, 10*time.Second, "the agent of "+id+" to end", func() bool { return ended.MatchString(ok(t, "ls")) })
}

// resume starts the agent of an ended session again, in the same session,
// with the cap asked for now. A session of new's default agent, the shell,
// continues with a login shell. Its errors say what to try: an agent that
// still runs, a session that does not exist, a bad size.
func TestResumeCommand(t *testing.T) {
	newTestHost(t)
	short := newSession(t, "short", "/bin/sh", "-c", "sleep 3; exit 2")
	failsWithHint(t, "the agent of short on box still runs", "attach box/"+short, "resume", "box/"+short, "--detach")
	failsWithHint(t, `no session "nosuch" on box`, "ls --host box", "resume", "box/nosuch")
	failsWithHint(t, `"100M" is below the smallest cap, 256M`, "size of 256M or more", "resume", "box/"+short, "--memory-max", "100M")

	agentEnded(t, short)
	out := ok(t, "resume", "box/short", "--detach", "--memory-max", "512M")
	if want := "Resumed box/" + short + " (short, shell): continued the latest conversation in its folder.\n"; out != want {
		t.Fatalf("resume printed %q, want %q", out, want)
	}
	if got := sessionCap(t, short); got != capFor(512<<20) {
		t.Errorf("resume --memory-max 512M: the cap is %d, want %d", got, capFor(512<<20))
	}
	if s := sessionState(t, short); s == api.StateExited || s == api.StateErrored {
		t.Errorf("after the resume the session is %s", s)
	}
	failsWithHint(t, "the agent of short on box still runs", "attach box/"+short, "resume", "box/"+short, "--detach")
}

// With a helper from before the resume, the command says that it is too
// old, and how to update it.
func TestResumeWithAnOldHelper(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "short", "/bin/sh", "-c", "exit 0")
	agentEnded(t, id)
	old := host.Features
	host.Features = slices.DeleteFunc(slices.Clone(old), func(f string) bool { return f == "resume" })
	t.Cleanup(func() { host.Features = old })
	failsWithHint(t, "the helper on box is too old to resume a session", "host add box", "resume", "box/"+id)
}

// Without --detach, resume attaches to the session, as new does.
func TestResumeAttaches(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "again", "/bin/sh", "-c", "exit 1")
	agentEnded(t, id)
	term, done := inTerminal(t, "resume", "box/"+id)
	term.WaitFor("\x1b[22;0t")
	term.Type("echo BACK-$((2+3))\r")
	term.WaitFor("BACK-5")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("Detached. again keeps running on box.")
}

// sessionState is the state of the session id, as the host lists it.
func sessionState(t *testing.T, id string) string {
	t.Helper()
	list, err := host.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s.State
		}
	}
	t.Fatalf("no session %s", id)
	return ""
}

// The host's manifest names the secrets that a resume fetches, so a host
// could ask for any secret in config.toml. A resume fetches without asking
// only those that hosts.toml gives the host. Another needs --secret, and
// until then its command does not run.
func TestResumeFetchesOnlyTheHostsSecrets(t *testing.T) {
	th := newTestHost(t)
	fetched := filepath.Join(th.h.Home, "fetched")
	writeSettings(t, "[secrets]\nGITHUB_TOKEN = \"touch '"+fetched+"'; echo gh\"\n")
	id := newSession(t, "short", "sleep", "60")
	pid := hosttest.HolderPID(t, id)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := hosttest.Reap(pid, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	m, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	m.Secrets = []string{"GITHUB_TOKEN"}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	failsWithHint(t, "short on box asks for secrets that hosts.toml does not give box: GITHUB_TOKEN",
		"resume box/"+id+" --secret GITHUB_TOKEN", "resume", "box/"+id, "--detach")
	if _, err := os.Stat(fetched); err == nil {
		t.Fatal("a secret that hosts.toml does not give the host was fetched")
	}
	ok(t, "resume", "box/"+id, "--detach", "--secret", "GITHUB_TOKEN")
	if _, err := os.Stat(fetched); err != nil {
		t.Errorf("--secret did not fetch the secret: %v", err)
	}
}
