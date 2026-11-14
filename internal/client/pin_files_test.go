package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestForgetHostRemovesOnlyItsPin(t *testing.T) {
	sshDirs(t)
	k := newKey(t, "ssh-ed25519")
	names := []string{"box", "box2", "Box3", "b"}
	hosts := make([]Host, 0, len(names))
	for _, n := range names {
		hosts = append(hosts, Host{Name: n, SSH: n})
		if err := writeTestPin(n, []hostKey{k}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveHosts(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost("box"); err != nil {
		t.Fatal(err)
	}
	if HasPin("box") || !HasPin("box2") || !HasPin("Box3") || !HasPin("b") {
		t.Error("ForgetHost removed the wrong pins")
	}
	// Two hosts whose names differ only in case have pins of their own,
	// also on a disk that ignores case: forgetting one keeps the other's.
	twin := newKey(t, "ssh-ed25519")
	if err := SaveHosts(context.Background(), []Host{{Name: "twin", SSH: "twin"}, {Name: "TWIN", SSH: "twin"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin("twin", []hostKey{twin}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin("TWIN", []hostKey{k}); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost("TWIN"); err != nil || HasPin("TWIN") {
		t.Errorf("ForgetHost(TWIN) = %v, and its pin is there: %v", err, HasPin("TWIN"))
	}
	if keys, err := readPin("twin"); err != nil || len(keys) != 1 || keys[0].Fingerprint() != twin.Fingerprint() {
		t.Errorf("the pin of twin: %v, %v", keys, err)
	}
}

// Malformed saved names: ForgetHost forgets them, and touches no file.
func TestForgetHostWithAMalformedName(t *testing.T) {
	sshDirs(t)
	k := newKey(t, "ssh-ed25519")
	if err := writeTestPin("victim", []hostKey{k}); err != nil {
		t.Fatal(err)
	}
	victim := pinPath("victim")
	for _, name := range []string{"x/../known_hosts-victim", "../ssh/known_hosts-victim", "a/b", `x\..\known_hosts-victim`, ".."} {
		if err := SaveHosts(context.Background(), []Host{{Name: name, SSH: "box"}, {Name: "victim", SSH: "victim"}}); err != nil {
			t.Fatal(err)
		}
		before := tree(t, ConfigDir())
		if err := ForgetHost(name); err != nil {
			t.Fatalf("ForgetHost(%q) = %v", name, err)
		}
		if _, err := FindHost(name); err == nil {
			t.Errorf("%q is still saved", name)
		}
		after := tree(t, ConfigDir())
		delete(before, "hosts.toml")
		delete(after, "hosts.toml")
		delete(before, "hosts.toml.lock")
		delete(after, "hosts.toml.lock")
		if !HasPin("victim") || len(before) != len(after) {
			t.Errorf("ForgetHost(%q) touched files: %v, then %v (victim's pin %s)", name, before, after, victim)
		}
		// Nor does a connection build a path from it.
		if _, err := writeWrapper(Host{Name: name, SSH: "box"}); err == nil {
			t.Errorf("writeWrapper(%q) wrote a file", name)
		}
	}
}

// tree lists the files under dir, with their sizes.
func tree(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out[rel] = info.Size()
		}
		return nil
	})
	return out
}

// exitState is the state of a process that exited with 255: this test
// binary, started again (init), so that it works on every system.
func exitState(t *testing.T) *os.ProcessState {
	t.Helper()
	var ee *exec.ExitError
	_ = errors.As(exitErrCode(t, 255), &ee)
	return ee.ProcessState
}

// exitErrCode is the ExitError of a process that exited with code.
func exitErrCode(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "PIN_TEST_EXIT="+strconv.Itoa(code))
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("the helper process (%d): %v", code, err)
	}
	return ee
}

// Two clients that pin one host at once: one reads the key, and the other
// keeps its pin. A waiting client that wants another fingerprint fails.
func TestTwoClientsPinOnce(t *testing.T) {
	dir := fakeSSH(t)
	k, other := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	shows(t, dir, k)
	put(t, dir, "slow", "")
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = pinHostKey(context.Background(), h, "", FirstUseAccept, false)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || fetches(t, dir) != 1 {
		t.Errorf("errors %v, %d fetches, want one", errs, fetches(t, dir))
	}
	if keys, err := readPin("box"); err != nil || len(keys) != 1 || keys[0].Fingerprint() != k.Fingerprint() {
		t.Errorf("pin %v, %v", keys, err)
	}
	// Two clients with different --host-key values: the one that waits
	// checks its own against the pin that won, and fails.
	if err := forgetPin("box"); err != nil {
		t.Fatal(err)
	}
	wants := []string{k.Fingerprint(), other.Fingerprint()}
	before := fetches(t, dir)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = pinHostKey(context.Background(), h, wants[i], FirstUseRefuse, false)
		}()
		time.Sleep(300 * time.Millisecond) // the first takes the lock, and works for a second
	}
	wg.Wait()
	var ue *UserError
	if errs[0] != nil || !errors.As(errs[1], &ue) || ue.Code != "host_key_pinned" || exitCodeOf(ue) != 5 {
		t.Errorf("errors %v", errs)
	}
	if n := fetches(t, dir) - before; n != 1 {
		t.Errorf("%d connections read the key, want one: the waiting client reads none", n)
	}
	if keys, err := readPin("box"); err != nil || keys[0].Fingerprint() != k.Fingerprint() {
		t.Errorf("pin %v, %v", keys, err)
	}
}

// exitCodeOf is the exit code that the command line gives a user error
// (docs/contributing/cli-spec.md 5.1).
func exitCodeOf(e *UserError) int {
	return map[Kind]int{KindFailed: 1, KindUsage: 2, KindNotFound: 3, KindNeedsAnswer: 4, KindRefused: 5, KindCancelled: 130}[e.Kind]
}

// writeTestPin gives fixtures the legacy current-stage choice explicitly.
func writeTestPin(name string, keys []hostKey) error { return writePin(name, currentStage(name), keys) }
