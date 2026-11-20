package testhome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// before is what the go command said of Go's folders before the home
// moved, or nil without a go command.
var before map[string]string

// usersFiles are the user's own Go settings, as they were before the
// tests: the telemetry mode file and the file of `go env -w`.
var usersFiles map[string]fileState

func TestMain(m *testing.M) {
	usersFiles = goSettings()
	before = goEnv()
	Main(m)
}

// fileState is a file as a test saw it.
type fileState struct {
	there bool
	data  string
	mod   time.Time
}

// goSettings reads the user's own Go settings files, where the go command
// finds them with the environment as it is now.
func goSettings() map[string]fileState {
	out := map[string]fileState{}
	config, err := os.UserConfigDir()
	if err != nil {
		return out
	}
	env := os.Getenv("GOENV")
	if env == "" {
		env = filepath.Join(config, "go", "env")
	}
	for _, p := range []string{filepath.Join(config, "go", "telemetry", "mode"), env} {
		st, err := os.Stat(p)
		if err != nil {
			out[p] = fileState{}
			continue
		}
		data, _ := os.ReadFile(p)
		out[p] = fileState{there: true, data: string(data), mod: st.ModTime()}
	}
	return out
}

// goEnv asks the go command where Go's folders are, and its telemetry
// mode. Before the home has moved, the go command runs as the one that
// runs the tests does, and goEnv writes nothing. In a home that testhome made (trusted), the go command
// runs with a config folder of its own, in the root, with its telemetry
// off: its counters, and a process of its own that writes them later, never
// reach the user's config folder, nor make the home again once it has
// gone. GOENV still names the user's file, which the go command only reads.
func goEnv() map[string]string {
	cmd := exec.Command("go", "env", "-json", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV", "GOTELEMETRY")
	if trusted(Root()) {
		quiet, ok := goQuiet()
		if !ok {
			return nil
		}
		// The last value of a name is the one that counts.
		cmd.Env = append(os.Environ(), quiet...)
	}
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	var env map[string]string
	if json.Unmarshal(b, &env) != nil {
		return nil
	}
	return env
}

// goQuiet is the environment of a go command, or of a test binary that
// runs one, in the tests' home: a config folder of its own in the root,
// where the go command's telemetry is off. It is false when the folder
// cannot be made.
func goQuiet() ([]string, bool) {
	home := filepath.Join(Root(), "gocmd")
	mode := filepath.Join(home, ".config", "go", "telemetry", "mode")
	switch runtime.GOOS {
	case "darwin":
		mode = filepath.Join(home, "Library", "Application Support", "go", "telemetry", "mode")
	case "windows":
		mode = filepath.Join(home, "AppData", "go", "telemetry", "mode")
	}
	if os.MkdirAll(filepath.Dir(mode), 0o700) != nil || os.WriteFile(mode, []byte("off"), 0o600) != nil {
		return nil, false
	}
	return []string{"HOME=" + home, "USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "APPDATA=" + filepath.Join(home, "AppData")}, true
}

// The tests run in a home of their own, with the XDG folders in it, and
// the go command still finds Go's folders where they were. Its telemetry
// is off there: the go command reads the config folder of its own that
// goEnv gave it.
func TestAHomeOfTheirOwn(t *testing.T) {
	Check(t, os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_DATA_HOME"), os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_CACHE_HOME"), os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("USERPROFILE"), os.Getenv("XDG_CONFIG_DIRS"), os.Getenv("XDG_DATA_DIRS"))
	if before == nil {
		t.Skip("no go command to ask")
	}
	got := goEnv()
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"} {
		if got[k] != before[k] {
			t.Errorf("%s is %q in the tests' home, and was %q", k, got[k], before[k])
		}
	}
	if before["GOTELEMETRY"] != "" && got["GOTELEMETRY"] != "off" {
		t.Errorf("the go command's telemetry is %q in the tests' home, want off", got["GOTELEMETRY"])
	}
}

// The tests leave the user's own Go settings as they were: the telemetry
// mode file and the file of `go env -w`, if any, have the same content and
// time, after the go command ran in the tests' home.
func TestTheUsersGoSettingsStayAsTheyWere(t *testing.T) {
	_ = goEnv()
	if len(usersFiles) == 0 {
		t.Skip("no config folder to look in")
	}
	for p, was := range usersFiles {
		var now fileState
		if st, err := os.Stat(p); err == nil {
			data, _ := os.ReadFile(p)
			now = fileState{there: true, data: string(data), mod: st.ModTime()}
		}
		if now.there != was.there || now.data != was.data || !now.mod.Equal(was.mod) {
			t.Errorf("the tests changed %s: it was %+v, and is %+v", p, was, now)
		}
	}
}

// The user's Go settings stay as they were also in a test binary that got
// a TESTHOME_ROOT that testhome did not make, "/" or a folder that is gone,
// where the go command runs before the home moves. The test starts this
// binary again so, with a home of its own that holds a telemetry mode file
// and a file of `go env -w`, and the child's sentinel test, and this test
// too, find both as they were.
func TestTheUsersGoSettingsStayInAChildWithAStrangeRoot(t *testing.T) {
	if os.Getenv("TESTHOME_CHILD") != "" {
		t.Skip("in the child")
	}
	if before == nil {
		t.Skip("no go command to ask")
	}
	for _, given := range []string{string(filepath.Separator), filepath.Join(filepath.Dir(Root()), "th0gone")} {
		planted := t.TempDir()
		config := filepath.Join(planted, ".config")
		switch runtime.GOOS {
		case "darwin":
			config = filepath.Join(planted, "Library", "Application Support")
		case "windows":
			config = filepath.Join(planted, "AppData")
		}
		files := []string{filepath.Join(config, "go", "telemetry", "mode"), filepath.Join(config, "go", "env")}
		long := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		for i, p := range files {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte([]string{"off", "GOTOOLCHAIN=local\n"}[i]), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(p, long, long); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestTheUsersGoSettingsStayAsTheyWere$", "-test.v")
		for _, kv := range os.Environ() {
			name, _, _ := strings.Cut(kv, "=")
			switch name {
			case rootVar, "HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME", "GOENV":
			default:
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "TESTHOME_CHILD=1", rootVar+"="+given, "HOME="+planted, "USERPROFILE="+planted, "APPDATA="+filepath.Join(planted, "AppData"),
			"GOCACHE="+before["GOCACHE"], "GOMODCACHE="+before["GOMODCACHE"], "GOPATH="+before["GOPATH"])
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestTheUsersGoSettingsStayAsTheyWere") {
			t.Errorf("given %q, the child: %v\n%s", given, err, out)
		}
		for i, p := range files {
			st, err := os.Stat(p)
			data, _ := os.ReadFile(p)
			if err != nil || !st.ModTime().Equal(long) || string(data) != []string{"off", "GOTOOLCHAIN=local\n"}[i] {
				t.Errorf("given %q, the child changed %s: %q, %v", given, p, data, err)
			}
		}
	}
}

// A test binary that a test starts again keeps the home it got.
func TestAChildKeepsItsHome(t *testing.T) {
	root, home := Root(), os.Getenv("HOME")
	done, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	done()
	if Root() != root || os.Getenv("HOME") != home {
		t.Errorf("a second Isolate moved the home from %s to %s", home, os.Getenv("HOME"))
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("a second Isolate removed the home: %v", err)
	}
}

// The home goes when the tests end. The test starts this binary again,
// without the home it has, or with a TESTHOME_ROOT that testhome did not
// make: "/", and a folder that is gone. The child makes a home of its own
// each time, names it, and removes it.
func TestTheHomeGoesAfterTheTests(t *testing.T) {
	if os.Getenv("TESTHOME_CHILD") != "" {
		fmt.Printf("root=%s\nhome=%s\n", Root(), os.Getenv("HOME"))
		return
	}
	for _, given := range []string{"", string(filepath.Separator), filepath.Join(Root()+"gone", "stale")} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTheHomeGoesAfterTheTests$")
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, rootVar+"=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		// The child's go command, before its home moves, runs in this
		// binary's home, with the telemetry off.
		quiet, ok := goQuiet()
		if !ok {
			t.Fatal("no config folder for the go command")
		}
		cmd.Env = append(append(cmd.Env, quiet...), "TESTHOME_CHILD=1")
		if given != "" {
			cmd.Env = append(cmd.Env, rootVar+"="+given)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the child: %v\n%s", err, out)
		}
		_, rest, _ := strings.Cut(string(out), "root=")
		root, rest, _ := strings.Cut(rest, "\n")
		_, rest, _ = strings.Cut(rest, "home=")
		home, _, _ := strings.Cut(rest, "\n")
		if root == "" || root == Root() || root == given || !under(root, home) {
			t.Fatalf("given %q, the child had the home %q in %q, and this binary has %q:\n%s", given, home, root, Root(), out)
		}
		if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("given %q, the child's home %s is still there: %v", given, root, err)
		}
	}
}

// A test binary trusts a TESTHOME_ROOT only for a folder that testhome
// made: in a temporary folder, with its marker.
func TestTrusted(t *testing.T) {
	plain, err := makeTemp(filepath.Dir(Root()), "th") // a th… folder without the marker
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(plain) })
	inside := filepath.Join(Root(), "th1") // a th… folder, with a marker, not in a temporary folder
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, marker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !trusted(Root()) {
		t.Errorf("the tests' own folder %s is not trusted", Root())
	}
	for _, root := range []string{"", string(filepath.Separator), filepath.Join(Root()+"gone", "stale"), plain, inside} {
		if trusted(root) {
			t.Errorf("trusted(%q)", root)
		}
	}
}

// The root is short, so the longest socket that a test makes in it fits:
// a shared SSH connection's, in the cache folder, and a session's, in the
// runtime folder. The root's length counts with its symlinks resolved: a
// short path that resolves to a long one makes way for the short folder.
func TestTheRootFitsItsSockets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no such limit")
	}
	if len(Root()) > maxRoot {
		t.Errorf("the root %s has %d bytes, more than %d", Root(), len(Root()), maxRoot)
	}
	for _, s := range []string{
		filepath.Join(os.Getenv("XDG_CACHE_HOME"), brand.Name, "cm", "nf-"+strings.Repeat("c", 40)),
		filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), brand.Name, "s", "abcdefgh.sock"),
	} {
		if err := os.MkdirAll(filepath.Dir(s), 0o700); err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("unix", s)
		if err != nil {
			t.Errorf("a socket of %d bytes in the root: %v", len(s), err)
			continue
		}
		_ = l.Close()
	}
	// The link's own path is short for certain, and what it names long.
	long := filepath.Join(t.TempDir(), strings.Repeat("x", maxRoot))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	links, err := os.MkdirTemp("/tmp", "l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(links) })
	link := filepath.Join(links, "l")
	if err := os.Symlink(long, link); err != nil {
		t.Fatal(err)
	}
	if n := len(link) + len("/th") + 10; n > maxRoot {
		t.Fatalf("the link %s is not short: a folder in it has up to %d bytes", link, n)
	}
	shortBase, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := tempDirIn(link, shortBase, "th")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if resolved, _ := filepath.EvalSymlinks(dir); dir != resolved || len(dir) > maxRoot || filepath.Dir(dir) != shortBase {
		t.Errorf("a short link to a long folder gave %s (%s), want a short folder in %s", dir, resolved, shortBase)
	}
}

// A medium-length temporary base fits the first root but not a second root
// nested within it. Exercise the socket fixture in that environment too.
func TestTheSocketFixtureWorksWithAMediumTempDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no such socket path limit")
	}
	if os.Getenv("TESTHOME_SOCKET_CHILD") == "1" {
		if n := len(os.TempDir()); n < 14 || n > 26 {
			t.Fatalf("the temporary base has %d bytes, want 14 to 26", n)
		}
		if filepath.Dir(Root()) != os.TempDir() {
			t.Fatalf("root %s is not in the selected temporary base %s", Root(), os.TempDir())
		}
		TestTheRootFitsItsSockets(t)
		return
	}
	shortBase, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	// An already private short temporary base can hold the same 26-byte
	// fixture. This also keeps its sockets inside a caller's owned test root.
	outer, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, statErr := os.Stat(outer)
	private := statErr == nil && st.IsDir() && st.Mode().Perm() == 0o700 && filepath.Dir(Root()) == outer
	if !private || len(outer) < 14 || len(outer) > 24 {
		outer, err = makeTemp(shortBase, strings.Repeat("m", max(0, 12-len(shortBase))))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(outer) })
	}
	medium := filepath.Join(outer, strings.Repeat("m", 25-len(outer)))
	if err := os.Mkdir(medium, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(medium) })
	if len(medium) != 26 {
		t.Fatalf("the temporary fixture has %d bytes, want 26", len(medium))
	}
	quiet, ok := goQuiet()
	if !ok {
		t.Fatal("no config folder for the child's go command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTheSocketFixtureWorksWithAMediumTempDir$", "-test.v")
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name != rootVar && name != "TMPDIR" && name != "TESTHOME_SOCKET_CHILD" {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(append(cmd.Env, quiet...), "TMPDIR="+medium, "TESTHOME_SOCKET_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestTheSocketFixtureWorksWithAMediumTempDir") {
		t.Errorf("the medium-TMPDIR child: %v\n%s", err, out)
	}
}

func TestUnder(t *testing.T) {
	root := filepath.Join(string(filepath.Separator)+"tmp", "th1")
	for path, want := range map[string]bool{
		root:                                  true,
		filepath.Join(root, "home", ".x"):     true,
		filepath.Join(root, ".."):             false,
		filepath.Join(root+"2", "home"):       false,
		filepath.Join(root, "..", "th2", "a"): false,
	} {
		if got := under(root, path); got != want {
			t.Errorf("under(%q, %q) = %v", root, path, got)
		}
	}
	if under("", root) {
		t.Error("a path is under no root")
	}
}
