package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// No path is built from a name that cannot name a host: host rekey of a
// hand-edited saved name that leads out of the folder is refused, and
// writes no file.
func TestNoPathFromABadName(t *testing.T) {
	sshDirs(t)
	bad := Host{Name: "../../escape", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{bad}); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(filepath.Dir(ConfigDir()))
	before := tree(t, parent)
	var ue *UserError
	if _, _, err := Rekey(context.Background(), bad, "", func(string, string) error { return nil }); !errors.As(err, &ue) || ue.Code != "bad_name" {
		t.Errorf("host rekey: %v", err)
	}
	if _, err := lockPinWait(bad.Name); !errors.As(err, &ue) || ue.Code != "bad_name" {
		t.Errorf("the pin's lock: %v", err)
	}
	if HostFileKey(bad.Name) != "" || lockFile(bad.Name) != "" || pinPath(bad.Name) != "" {
		t.Error("a file name from a bad name")
	}
	if after := tree(t, parent); len(after) != len(before) {
		t.Errorf("files were written: %v, then %v", before, after)
	}
}

// A connection builds its options from the one snapshot of the pin that
// decided that the host has one. When another client forgets the host
// right after that snapshot, the command still checks the pin, whose file
// is gone, so ssh refuses: it never goes without the pin's options.
func TestForgetBetweenTheCheckAndTheCommand(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, "box")
	read := readPinLocked
	t.Cleanup(func() { readPinLocked = read })
	readPinLocked = func(ctx context.Context, name string, f func()) error {
		err := read(ctx, name, f)
		if ferr := ForgetHostContext(ctx, name); ferr != nil { // another client, just after
			t.Errorf("ForgetHost: %v", ferr)
		}
		return err
	}
	cmd, err := (&SSH{Host: h}).Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")
	for _, o := range []string{"UserKnownHostsFile=" + sshPathArg(pinPath("box"), runtime.GOOS), "StrictHostKeyChecking=yes", "HostKeyAlias="} {
		if !strings.Contains(line, o) {
			t.Errorf("the command lacks %s: %s", o, line)
		}
	}
	if HasPin("box") {
		t.Error("the pin is still there")
	}
	// The next command, after the forget, does not start.
	readPinLocked = read
	var ue *UserError
	if _, err := (&SSH{Host: h}).Command(context.Background(), "true"); !errors.As(err, &ue) || ue.Code != "host_key_unpinned" {
		t.Errorf("after the forget: %v", err)
	}
}

// The launcher keeps a host in memory. When host rm forgets it in a
// terminal, the launcher's next connection to it, a redial at a terminal,
// is refused: no pin, no connection.
func TestRedialAfterHostRm(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "trusted"))
	put(t, dir, "values", values("no"))
	shows(t, dir, k)
	if err := SaveHosts(context.Background(), []Host{{Name: "box", SSH: "box.example"}}); err != nil {
		t.Fatal(err)
	}
	h, err := FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := HostTransport(context.Background(), h, true); err != nil || !HasPin("box") {
		t.Fatalf("the first connection: %v, pinned %v", err, HasPin("box"))
	}
	if err := ForgetHost("box"); err != nil {
		t.Fatal(err)
	}
	n := len(sshCalls(t, dir))
	for _, interactive := range []bool{false, true} {
		ctx := context.Background()
		if !interactive {
			ctx = WithoutWake(WithoutInput(ctx))
		}
		_, err = Dial(ctx, h, interactive)
		var ue *UserError
		if !errors.As(err, &ue) || ue.Code != "host_key_unpinned" || HasPin("box") {
			t.Errorf("the redial (interactive=%v): %v, pinned %v", interactive, err, HasPin("box"))
		}
	}
	for _, c := range sshCalls(t, dir)[n:] {
		if !strings.Contains(c, " -G ") && !hasOption(c, "PreferredAuthentications=none") {
			t.Errorf("a connection that can log in: %q", c)
		}
	}
}

// A file at a host's pin path that is the pin of another host, or names no
// host, is never used, replaced or removed.
func TestAnotherHostsPinIsRefused(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	other := newKey(t, "ssh-ed25519")
	if err := os.MkdirAll(sshDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{pinContent("BOX", []hostKey{other}), []byte(strings.Replace(string(pinContent("box", []hostKey{other})), pinHeader("box"), "# a pin", 1))} {
		if err := os.WriteFile(pinPath("box"), body, 0o600); err != nil {
			t.Fatal(err)
		}
		var ue *UserError
		if _, err := (&SSH{Host: h}).Command(context.Background(), "true"); !errors.As(err, &ue) || ue.Code != "host_key_unreadable" || ue.Kind != KindRefused {
			t.Errorf("a command: %v", err)
		}
		if _, err := readPin("box"); !isForeign(err) {
			t.Errorf("readPin: %v", err)
		}
		if c, ok := PinCheck(h); !ok || c.OK || !strings.Contains(c.Detail, "so the client does not use it") {
			t.Errorf("host doctor: %+v", c)
		}
		if err := StagePin(context.Background(), "box"); err != nil {
			t.Fatal(err)
		}
		if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
			t.Fatal(err)
		}
		if err := SaveHostPin(h); !isForeign(err) {
			t.Errorf("SaveHostPin over it: %v", err)
		}
		unlock, err := lockPinWait("box")
		if err != nil {
			t.Fatal(err)
		}
		err = forgetPin("box")
		unlock()
		if after, _ := os.ReadFile(pinPath("box")); !isForeign(err) || string(after) != string(body) {
			t.Errorf("forgetPin: %v, the file %q", err, after)
		}
	}
}

// A host reached over SSH is never saved without a pin.
func TestNoSaveWithoutAPin(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	var ue *UserError
	if err := SaveHostPin(h); !errors.As(err, &ue) || ue.Code != "host_key_unpinned" || isSaved(t, "box") {
		t.Errorf("no stage: %v, saved %v", err, isSaved(t, "box"))
	}
	if err := StagePin(context.Background(), "box"); err != nil { // a stage that holds no key yet
		t.Fatal(err)
	}
	if err := SaveHostPin(h); !errors.As(err, &ue) || ue.Code != "host_key_unpinned" || isSaved(t, "box") {
		t.Errorf("an empty stage: %v, saved %v", err, isSaved(t, "box"))
	}
	// A host with no SSH is saved, and one whose pin is there already.
	if err := SaveHostPin(Host{Name: "c1", Machine: &Machine{Provider: "docker", ID: "c1"}}); err != nil || !isSaved(t, "c1") {
		t.Errorf("a container: %v", err)
	}
	if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	if err := SaveHostPin(h); err != nil || !isSaved(t, "box") {
		t.Errorf("a host with its pin: %v", err)
	}
}

// The save of a host with its pin is one step: when either half fails,
// hosts.toml and the pin are as they were, and no host is saved without
// its pin.
func TestFailedPublication(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	orphan := pinContent("box", []hostKey{newKey(t, "ssh-ed25519")})
	failure := errors.New("the disk is full")
	for _, c := range []struct {
		name  string
		setup func()
	}{
		{"the pin's move fails", func() { publishPin = func(string, string) error { return failure } }},
		{"the save fails after the move", func() { writeHosts = func([]Host) error { return failure } }},
	} {
		t.Run(c.name, func(t *testing.T) {
			pub, save := publishPin, writeHosts
			t.Cleanup(func() { publishPin, writeHosts = pub, save })
			for _, before := range [][]byte{nil, orphan} {
				_ = os.Remove(pinPath("box"))
				if before != nil {
					if err := os.WriteFile(pinPath("box"), before, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := StagePin(context.Background(), "box"); err != nil {
					t.Fatal(err)
				}
				if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
					t.Fatal(err)
				}
				c.setup()
				err := SaveHostPin(h)
				publishPin, writeHosts = pub, save
				after, rerr := os.ReadFile(pinPath("box"))
				if !errors.Is(err, failure) || isSaved(t, "box") {
					t.Errorf("before %v: %v, saved %v", before != nil, err, isSaved(t, "box"))
				}
				if before == nil && rerr == nil || before != nil && string(after) != string(before) {
					t.Errorf("before %v: the pin is %q, %v", before != nil, after, rerr)
				}
			}
		})
	}
}

// AbortPin waits for an operation that pins the host, and an operation
// whose stage closed, or was replaced, before it took the lock writes
// nothing.
func TestAbortWaitsForThePin(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	shows(t, dir, k)
	put(t, dir, "slow", "")
	h := Host{Name: "box", SSH: "box.example"}
	if err := StagePin(context.Background(), "box"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := pinHostKey(context.Background(), h, "", FirstUseAccept, true)
		done <- err
	}()
	heldBy(t, "box")
	AbortPin("box") // waits for the pin, then closes the stage
	if err := <-done; err != nil {
		var ue *UserError
		if !errors.As(err, &ue) || ue.Code != "cancelled" {
			t.Errorf("the pin: %v", err)
		}
	}
	if HasPin("box") || staging("box") {
		t.Errorf("after the abort: pinned %v, staging %v", HasPin("box"), staging("box"))
	}

	// The stage closes while the operation waits for the lock.
	if err := StagePin(context.Background(), "box"); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockPinWait("box")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	ps := pinStarted
	t.Cleanup(func() { pinStarted = ps })
	pinStarted = func(string) { close(started) }
	go func() {
		_, err := pinHostKey(context.Background(), h, "", FirstUseAccept, true)
		done <- err
	}()
	<-started
	newer := &stage{path: currentStage("box").path + "-newer"} // a newer add
	stagesMu.Lock()
	stages["box"] = newer
	stagesMu.Unlock()
	unlock()
	var ue *UserError
	if err := <-done; !errors.As(err, &ue) || ue.Code != "cancelled" || HasPin("box") || len(currentStage("box").keys) != 0 {
		t.Errorf("an operation whose stage was replaced: %v, pinned %v", err, HasPin("box"))
	}
	stagesMu.Lock()
	delete(stages, "box")
	stagesMu.Unlock()
}

// VerboseLog builds its ssh invocation from the single locked snapshot too:
// when another client forgets the host right after the snapshot (and another
// reader writes config-box back), VerboseLog still passes the pin's options
// after -v, and refuses after the forget.
func TestVerboseLogKeepsItsPinSnapshot(t *testing.T) {
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, "box")
	put(t, dir, "stderr", "debug1: Connecting to box.example\r\nHost key verification failed.\r\n")
	wantPin := sshPathArg(pinPath("box"), runtime.GOOS)

	read := readPinLocked
	t.Cleanup(func() { readPinLocked = read })
	first := true
	readPinLocked = func(ctx context.Context, name string, f func()) error {
		err := read(ctx, name, f)
		if first {
			first = false
			if ferr := ForgetHostContext(ctx, name); ferr != nil {
				t.Errorf("ForgetHost: %v", ferr)
			}
			// Simulate concurrent config inspection writing config-box back.
			if _, werr := writeWrapper(h); werr != nil {
				t.Errorf("writeWrapper: %v", werr)
			}
		}
		return err
	}
	logOut, err := (&SSH{Host: h}).VerboseLog(context.Background())
	if err != nil {
		t.Fatalf("VerboseLog: %v", err)
	}
	if !strings.Contains(string(logOut), "debug1:") {
		t.Errorf("VerboseLog lost debug lines: %q", logOut)
	}
	calls := slices.DeleteFunc(sshCalls(t, dir), func(c string) bool { return strings.Contains(c, " -O exit ") })
	if len(calls) != 1 {
		t.Fatalf("calls = %q, want 1", calls)
	}
	line := " " + calls[0] + " "
	for _, o := range []string{" -v ", " -o StrictHostKeyChecking=yes ", " -o UserKnownHostsFile=" + wantPin + " ", " -o HostKeyAlias="} {
		if !strings.Contains(line, o) {
			t.Errorf("VerboseLog lacks %q: %s", o, calls[0])
		}
	}
	if strings.Index(line, " -v ") > strings.Index(line, " -o LogLevel=ERROR ") {
		t.Errorf("-v came after LogLevel=ERROR, so OpenSSH would hide debug1: %s", calls[0])
	}
	readPinLocked = read
	var ue *UserError
	if _, err := (&SSH{Host: h}).VerboseLog(context.Background()); !errors.As(err, &ue) || ue.Code != "host_key_unpinned" {
		t.Errorf("after the forget: %v", err)
	}
}

// A connection, StagePin or ForgetHostContext with a short or cancelled
// context stops right away instead of waiting ten minutes on the pin's lock.
func TestPinLockHonoursContext(t *testing.T) {
	sshDirs(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, "box")

	// Even when the lock is free, an already-cancelled context fails closed.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlock, err := lockPin(cctx, "box"); !errors.Is(err, context.Canceled) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("lockPin on a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := (&SSH{Host: h}).Command(cctx, "true"); !errors.Is(err, context.Canceled) {
		t.Errorf("Command on a cancelled context = %v", err)
	}
	if err := StagePin(cctx, "box"); !errors.Is(err, context.Canceled) || staging("box") {
		t.Errorf("StagePin on a cancelled context = %v, staging=%v", err, staging("box"))
	}
	if err := ForgetHostContext(cctx, "box"); !errors.Is(err, context.Canceled) || !HasPin("box") || !isSaved(t, "box") {
		t.Errorf("ForgetHostContext on a cancelled context = %v, pinned=%v, saved=%v", err, HasPin("box"), isSaved(t, "box"))
	}

	// While an exclusive lock is held, short-deadline callers time out fast.
	unlock, err := lockPinWait("box")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, op := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"Command", func(ctx context.Context) error { _, err := (&SSH{Host: h}).Command(ctx, "true"); return err }},
		{"Dial", func(ctx context.Context) error { _, err := Dial(WithoutInput(ctx), h, false); return err }},
		{"StagePin", func(ctx context.Context) error { return StagePin(ctx, "box") }},
		{"ForgetHostContext", func(ctx context.Context) error { return ForgetHostContext(ctx, "box") }},
	} {
		tctx, stop := context.WithTimeout(context.Background(), 60*time.Millisecond)
		start := time.Now()
		err := op.run(tctx)
		stop()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
			t.Errorf("%s: %v after %s, want DeadlineExceeded quickly", op.name, err, time.Since(start))
		}
	}
	if staging("box") || !HasPin("box") || !isSaved(t, "box") {
		t.Errorf("state changed under held lock: staging=%v pinned=%v saved=%v", staging("box"), HasPin("box"), isSaved(t, "box"))
	}
}

// ForgetHost removes the pin before updating hosts.toml, so a failure
// removing the generated wrapper leaves no connectable host and can be
// retried. An orphan published pin for an unsaved host is refused too.
func TestForgetFailureLeavesNoConnectableHostAndRetries(t *testing.T) {
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	ctx := context.Background()
	var ue *UserError

	// An orphan published pin on disk without a saved host is refused.
	if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	if !HasPin("box") || isSaved(t, "box") {
		t.Fatalf("setup: pinned=%v saved=%v", HasPin("box"), isSaved(t, "box"))
	}
	for name, fn := range map[string]func() error{
		"Command":       func() error { _, err := (&SSH{Host: h}).Command(ctx, "true"); return err },
		"VerboseLog":    func() error { _, err := (&SSH{Host: h}).VerboseLog(ctx); return err },
		"GitSSHCommand": func() error { _, err := GitSSHCommand(ctx, h); return err },
		"Dial":          func() error { _, err := Dial(WithoutInput(ctx), h, false); return err },
	} {
		if err := fn(); !errors.As(err, &ue) || ue.Code != "host_key_unpinned" {
			t.Errorf("orphan pin (%s): %v, want host_key_unpinned", name, err)
		}
	}
	if calls := sshCalls(t, dir); len(calls) != 0 {
		t.Errorf("orphan pin ran ssh: %q", calls)
	}

	// Save box with its pin, then obstruct wrapperPath with a non-empty folder.
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, "box")
	wp, err := writeWrapper(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(wp); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wp, "busy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost("box"); err == nil {
		t.Fatal("ForgetHost succeeded despite obstructed wrapper folder")
	}
	if HasPin("box") {
		t.Fatal("ForgetHost left the pin on disk when wrapper removal failed")
	}
	if !isSaved(t, "box") {
		t.Fatal("ForgetHost removed box from hosts.toml before cleanup succeeded, breaking retry")
	}
	if err := os.RemoveAll(wp); err != nil {
		t.Fatal(err)
	}
	if _, err := writeWrapper(h); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SSH{Host: h}).Command(ctx, "true"); !errors.As(err, &ue) || ue.Code != "host_key_unpinned" {
		t.Errorf("after partial ForgetHost: %v, want host_key_unpinned", err)
	}
	// Retrying ForgetHost now finishes cleanly.
	if err := ForgetHost("box"); err != nil {
		t.Fatalf("retry ForgetHost: %v", err)
	}
	if HasPin("box") || isSaved(t, "box") {
		t.Errorf("after retry: pinned=%v saved=%v", HasPin("box"), isSaved(t, "box"))
	}
	if _, err := os.Stat(wp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("wrapper remained after retry: %v", err)
	}

	// ForgetHost on an unsaved name still sweeps any orphan pin before host_not_found.
	if err := writeTestPin("ghost", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost("ghost"); !errors.As(err, &ue) || ue.Code != "host_not_found" || HasPin("ghost") {
		t.Errorf("ForgetHost(ghost) = %v, pinned=%v", err, HasPin("ghost"))
	}
}
