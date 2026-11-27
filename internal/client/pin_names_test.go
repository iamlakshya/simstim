package client

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A second client, for the tests that need two: this test binary, started
// again with PIN_TEST_CLIENT set (init in pin_test.go).
func init() {
	if mode := os.Getenv("PIN_TEST_CLIENT"); mode == "forget-add" || mode == "forget" {
		if err := forgetAndAdd(mode == "forget"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

// forgetAndAdd is host rm box, then, unless only, host add box with the
// key that PIN_TEST_KEY holds, as another client runs them.
func forgetAndAdd(only bool) error {
	if err := ForgetHost("box"); err != nil || only {
		return err
	}
	_, k, ok := parseKeyLine("box " + os.Getenv("PIN_TEST_KEY"))
	if !ok {
		return errors.New("PIN_TEST_KEY holds no key")
	}
	if err := StagePin(context.Background(), "box"); err != nil {
		return err
	}
	defer AbortPin("box")
	if err := writeTestPin("box", []hostKey{k}); err != nil {
		return err
	}
	return SaveHostPin(Host{Name: "box", SSH: "box.example"})
}

// A pair of names that differ only in case, whose SHA-256 sums start with
// the same 8 hex characters: the file key of round 2 gave them one file.
var crafted = [2]string{"aBCDEfgHijKlMnoPqrstuvwxyzabcdef", "abcdEFGhijklMnopQrstuvwxyzabcdef"}

// hostFiles are the names of every file of the host called name that the
// pins write: the pin, its lock, and the prefixes of its temporary and
// staged files.
func hostFiles(name string) []string {
	return []string{pinPath(name), lockFile(name), ".known_hosts-" + HostFileKey(name) + "-", ".known_hosts-" + HostFileKey(name) + "-staged-"}
}

// Each host's files have a name of their own: two names that differ only
// in case never share one, not even on a disk that ignores case, and not
// even names made to collide in a short hash.
func TestHostFileKey(t *testing.T) {
	sshDirs(t)
	shape := regexp.MustCompile(`^[a-z0-9._-]{1,32}-[0-9a-f]{32}$`)
	long := "a" + strings.Repeat("b", 30) + ".-.-x"
	for _, n := range []string{"box", "BOX", "Box", "my.host_1", "a", long, crafted[0], crafted[1]} {
		k := HostFileKey(n)
		prefix := strings.ToLower(n)
		if len(prefix) > 32 {
			prefix = prefix[:32]
		}
		if !shape.MatchString(k) || !strings.HasPrefix(k, strings.TrimRight(prefix, ".-")+"-") || strings.HasSuffix(strings.Split(k, "-")[0], ".") {
			t.Errorf("HostFileKey(%q) = %q", n, k)
		}
	}
	if k := HostFileKey(long); !strings.HasPrefix(k, "a"+strings.Repeat("b", 30)+"-") {
		t.Errorf("a name cut at a dot: %s", k)
	}
	if HostFileKey("box") != "box-26f8567f2569182294c3fa5b9f9cb227" { // SHA-256 of "box"
		t.Errorf("HostFileKey(box) = %s", HostFileKey("box"))
	}
	for _, pair := range [][2]string{{"box", "BOX"}, {"box", "Box"}, crafted} {
		a, b := hostFiles(pair[0]), hostFiles(pair[1])
		for i := range a {
			if strings.EqualFold(a[i], b[i]) {
				t.Errorf("%s and %s share a file on a disk that ignores case: %s", pair[0], pair[1], a[i])
			}
		}
	}
}

// Windows drops a dot or a space at the end of a file's name, so such a
// name would share the files of another host: no host has one.
func TestTrailingDotRefused(t *testing.T) {
	for _, n := range []string{"box", "b", "box.example", "box-1", "box_", "0"} {
		if !ValidHostName(n) {
			t.Errorf("ValidHostName(%q) = false", n)
		}
	}
	for _, n := range []string{"box.", "box..", "box" + " ", ".", "a.", "box.\t"} {
		if ValidHostName(n) {
			t.Errorf("ValidHostName(%q) = true", n)
		}
	}
	sshDirs(t)
	if pinPath("box.") != "" || lockFile("box") == "" {
		t.Error("a path for box.")
	}
	if _, err := writeWrapper(Host{Name: "box.", SSH: "box"}); err == nil {
		t.Error("writeWrapper(box.) wrote a file")
	}
}

// Two clients add box and BOX at once: both have staged their pins before
// either saves. Each keeps its own pin.
func TestCaseTwinsAddAtOnce(t *testing.T) {
	for _, pair := range [][2]string{{"box", "BOX"}, crafted} {
		t.Run(pair[1], func(t *testing.T) { addTwinsAtOnce(t, pair[0], pair[1]) })
	}
}

// addTwinsAtOnce adds first and second at once, first's save first, and
// checks that every file on disk stays distinct even when case is folded.
func addTwinsAtOnce(t *testing.T, first, second string) {
	sshDirs(t)
	keys := map[string]hostKey{first: newKey(t, "ssh-ed25519"), second: newKey(t, "ssh-ed25519")}
	var staged, checked, wg sync.WaitGroup
	var checkOnce sync.Once
	staged.Add(len(keys))
	checked.Add(1)
	saveBox := make(chan struct{})
	errs := make(chan error, len(keys))
	for name, k := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer AbortPin(name)
			err := StagePin(context.Background(), name)
			if err == nil {
				err = writeTestPin(name, []hostKey{k})
			}
			staged.Done()
			staged.Wait() // the barrier: both pins are staged, and no host is saved
			checkOnce.Do(func() {
				checkCaseFoldedDir(t, sshDir())
				checked.Done()
			})
			checked.Wait()
			if name == second {
				<-saveBox // first saves first, then second
			}
			if err == nil {
				err = SaveHostPin(Host{Name: name, SSH: "box.example"})
			}
			if name == first {
				close(saveBox)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	checkCaseFoldedDir(t, sshDir())
	for name, k := range keys {
		got, err := readPin(name)
		if err != nil || len(got) != 1 || got[0].Fingerprint() != k.Fingerprint() || !isSaved(t, name) {
			t.Errorf("%s: pin %v, %v, saved %v", name, got, err, isSaved(t, name))
		}
	}
	if err := ForgetHost(second); err != nil || HasPin(second) {
		t.Errorf("ForgetHost(%s) = %v, pinned %v", second, err, HasPin(second))
	}
	if got, err := readPin(first); err != nil || len(got) != 1 || got[0].Fingerprint() != keys[first].Fingerprint() {
		t.Errorf("after ForgetHost(%s), %s's pin is %v, %v", second, first, got, err)
	}
}

// checkCaseFoldedDir simulates a disk that folds case by checking that no
// two entries in dir share a lower-case name, and that every pin, stage and
// lock file carries a HostFileKey.
func checkCaseFoldedDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	folded := map[string]string{}
	fileKeyInName := regexp.MustCompile(`[a-z0-9._-]{1,32}-[0-9a-f]{32}`)
	for _, e := range entries {
		low := strings.ToLower(e.Name())
		if prev, dup := folded[low]; dup {
			t.Errorf("two files collide when case is folded: %s and %s", prev, e.Name())
		}
		folded[low] = e.Name()
		if e.Name() != "default" && !fileKeyInName.MatchString(e.Name()) {
			t.Errorf("a file in %s lacks HostFileKey: %s", dir, e.Name())
		}
	}
}

// ForgetHost keeps the pin's lock file: a client that holds it, or waits
// for it, locks the same file as every client after it.
func TestForgetKeepsTheLock(t *testing.T) {
	sshDirs(t)
	if err := SaveHosts(context.Background(), []Host{{Name: "box", SSH: "box.example"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockPinWait("box")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	before, err := os.Stat(lockFile("box"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost("box"); err != nil || HasPin("box") {
		t.Fatalf("ForgetHost = %v, pinned %v", err, HasPin("box"))
	}
	if after, err := os.Stat(lockFile("box")); err != nil || !os.SameFile(before, after) {
		t.Errorf("the lock file changed: %v", err)
	}
}

// heldBy waits until a client holds the pin lock of name.
func heldBy(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f, err := os.OpenFile(lockFile(name), os.O_RDWR, 0)
		if err == nil {
			ok, _ := tryLockFile(f, true)
			if ok {
				_ = unlockFile(f)
			}
			_ = f.Close()
			if !ok {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no client took the lock of %s", name)
}

// While one client pins box, another forgets box and adds it again with
// another key. The second waits for the first, so the pin of the host that
// is saved in the end is the key that its add pinned: no pin is lost, and
// none is replaced without host rekey.
func TestRemoveAndAddWhileAnotherClientPins(t *testing.T) {
	dir := fakeSSH(t)
	first, second := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(first, "unknown"))
	shows(t, dir, first)
	put(t, dir, "slow", "") // the first client holds the lock for a second or two
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinned := make(chan error, 1)
	go func() {
		_, err := pinHostKey(context.Background(), h, "", FirstUseAccept, false)
		pinned <- err
	}()
	heldBy(t, "box")
	other := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	other.Env = append(os.Environ(), "PIN_TEST_CLIENT=forget-add", "PIN_TEST_KEY="+authorized(second))
	out, err := other.CombinedOutput()
	if err != nil {
		t.Fatalf("the other client: %v: %s", err, out)
	}
	if err := <-pinned; err != nil {
		t.Errorf("the first client: %v", err)
	}
	keys, err := readPin("box")
	if err != nil || len(keys) != 1 || keys[0].Fingerprint() != second.Fingerprint() || !isSaved(t, "box") {
		t.Errorf("box's pin %v, %v: want the key of the second add, %s", keys, err, second.Fingerprint())
	}

	// host rm alone, while the first client pins: it waits, and removes the
	// pin that the first client wrote. No pin outlives the host.
	if err := forgetPin("box"); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := pinHostKey(context.Background(), h, "", FirstUseAccept, false)
		pinned <- err
	}()
	heldBy(t, "box")
	rm := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	rm.Env = append(os.Environ(), "PIN_TEST_CLIENT=forget")
	if out, err := rm.CombinedOutput(); err != nil {
		t.Fatalf("host rm: %v: %s", err, out)
	}
	<-pinned
	if HasPin("box") || isSaved(t, "box") {
		t.Errorf("after host rm: pinned %v, saved %v", HasPin("box"), isSaved(t, "box"))
	}
}

// A client that waited for the lock while host rm forgot the host writes
// no pin: the host is gone.
func TestNoPinForAForgottenHost(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	shows(t, dir, k)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockPinWait("box") // host rm, at work
	if err != nil {
		t.Fatal(err)
	}
	pinned := make(chan error, 1)
	go func() {
		_, err := pinHostKey(context.Background(), h, "", FirstUseAccept, false)
		pinned <- err
	}()
	if err := SaveHosts(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	unlock()
	var ue *UserError
	if err := <-pinned; !errors.As(err, &ue) || ue.Code != "host_not_found" || HasPin("box") || fetches(t, dir) != 0 {
		t.Errorf("a pin for a forgotten host: %v, pinned %v, %d fetches", err, HasPin("box"), fetches(t, dir))
	}
}

// A pin of the first version, named after the host alone, moves to its new
// name at the host's next connection, under the lock, once.
func TestMigratePin(t *testing.T) {
	sshDirs(t)
	k, newer := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h, {Name: "BOX", SSH: "box.example"}}); err != nil {
		t.Fatal(err)
	}
	old := legacyPinPath("box")
	oldPin := "# The pinned host key of box.\n" + k.line(pinAlias("box")) + "\n"
	if err := os.MkdirAll(sshDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, sshDir(), "known_hosts-box", oldPin)
	if needed, err := needsPin(context.Background(), h); err != nil || needed {
		t.Fatal("the old pin did not move")
	}
	if keys, err := readPin("box"); err != nil || len(keys) != 1 || keys[0].Fingerprint() != k.Fingerprint() {
		t.Errorf("the moved pin: %v, %v", keys, err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old pin is still there: %v", err)
	}
	// box's old pin is not BOX's, though a disk that ignores case shows
	// it under BOX's old name too.
	put(t, sshDir(), "known_hosts-box", oldPin)
	migratePin("BOX")
	if HasPin("BOX") {
		t.Error("BOX took box's old pin")
	}
	// A host that has a new pin keeps it, and the old file stays.
	if err := writeTestPin("box", []hostKey{newer}); err != nil {
		t.Fatal(err)
	}
	migratePin("box")
	if keys, _ := readPin("box"); len(keys) != 1 || keys[0].Fingerprint() != newer.Fingerprint() {
		t.Errorf("the migration replaced a pin: %v", keys)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the old pin went, though it is not the pin: %v", err)
	}
	// host rm removes both.
	if err := ForgetHost("box"); err != nil || HasPin("box") {
		t.Fatalf("ForgetHost = %v", err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("host rm left the old pin: %v", err)
	}
	// An old pin of a host that is not saved stays where it is.
	put(t, sshDir(), "known_hosts-box", oldPin)
	migratePin("box")
	if HasPin("box") {
		t.Error("an unsaved host got a pin")
	}
	checkPortablePinPaths(t)
}

// sshDir is the folder of the pins.
func sshDir() string { return filepath.Dir(pinPath("x")) }

// checkPortablePinPaths verifies that pin_file.go and sshDir build and strip
// pin paths with filepath helpers rather than literal path separators, so a
// slash-only mutant fails on Unix too.
func checkPortablePinPaths(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	pf, err := parser.ParseFile(fset, "pin_file.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(pf, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err == nil && (strings.Contains(s, "/known_hosts") || strings.Contains(s, `\known_hosts`) || strings.Contains(s, "/ssh")) {
			t.Errorf("pin_file.go uses a literal separator in %q", s)
		}
		return true
	})
	tf, err := parser.ParseFile(fset, "pin_names_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range tf.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "sshDir" {
			continue
		}
		usedDir := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Dir" {
				usedDir = true
			}
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, err := strconv.Unquote(lit.Value)
				if err == nil && strings.ContainsAny(s, `/\`) {
					t.Errorf("sshDir uses a literal separator %q instead of filepath.Dir", s)
				}
			}
			return true
		})
		if !usedDir {
			t.Error("sshDir does not use filepath.Dir")
		}
	}
}

// A hosts.toml that cannot be read after the host was loaded: no
// connection that can log in starts, and none goes without a pin.
func TestHostListErrorStopsTheConnection(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "trusted"))
	put(t, dir, "values", values("accept-new"))
	shows(t, dir, k)
	if err := SaveHosts(context.Background(), []Host{{Name: "box", SSH: "box.example"}}); err != nil {
		t.Fatal(err)
	}
	h, err := FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath(), []byte("hosts = [ this is not TOML\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := &SSH{Host: h}
	_, e1 := Dial(ctx, h, true)
	_, e2 := HostTransport(ctx, h, true)
	_, e3 := s.Command(ctx, "true")
	_, e4 := s.Shell(ctx)
	_, e5 := s.Forward(ctx, nil)
	_, e6 := GitSSHCommand(ctx, h)
	_, e7 := s.VerboseLog(ctx)
	for i, err := range []error{e1, e2, e3, e4, e5, e6, e7} {
		if err == nil {
			t.Errorf("command %d went on", i+1)
		}
	}
	for _, c := range sshCalls(t, dir) {
		if !strings.Contains(c, " -G ") && !hasOption(c, "PreferredAuthentications=none") {
			t.Errorf("a connection that can log in: %q", c)
		}
	}
	if HasPin("box") {
		t.Error("a pin for a host that hosts.toml may not hold")
	}
}
