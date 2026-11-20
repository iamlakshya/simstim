package client

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
)

// setHome points the home folder at a new temporary folder, on every
// system, and returns it as the file system names it.
func setHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func mkFile(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func symlink(t *testing.T, target, link string) bool {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			return false // Windows may not let a test make a link
		}
		t.Fatal(err)
	}
	return true
}

func TestPrivateFiles(t *testing.T) {
	home := setHome(t)
	other := t.TempDir()
	key := mkFile(t, filepath.Join(home, ".ssh", "id_ed25519"))
	cases := []struct{ path, why string }{
		{key, "is in ~/.ssh, a private folder"},
		{mkFile(t, filepath.Join(home, ".ssh", "config")), "is in ~/.ssh, a private folder"},
		{mkFile(t, filepath.Join(home, ".aws", "config")), "is in ~/.aws, a private folder"},
		{mkFile(t, filepath.Join(home, ".config", "gh", "hosts.yml")), "is in ~/.config, a private folder"},
		{mkFile(t, filepath.Join(home, ".gnupg", "pubring.kbx")), "is in ~/.gnupg, a private folder"},
		{mkFile(t, filepath.Join(home, ".kube", "config")), "is in ~/.kube, a private folder"},
		{mkFile(t, filepath.Join(home, ".docker", "config.json")), "is in ~/.docker, a private folder"},
		{mkFile(t, filepath.Join(home, "Library", "Keychains", "login.keychain-db")), "is in ~/Library/Keychains, a private folder"},
		{mkFile(t, filepath.Join(other, ".env")), "has a private name (.env)"},
		{mkFile(t, filepath.Join(other, "server.pem")), "has a private name (*.pem)"},
		{mkFile(t, filepath.Join(other, "tls.KEY")), "has a private name (*.key)"},
		{mkFile(t, filepath.Join(other, "id_rsa.pub")), "has a private name (id_*)"},
		{mkFile(t, filepath.Join(other, "credentials.json")), "has a private name (credentials*)"},
		// Question 109: every .env.* file, but the three that hold no secret.
		{mkFile(t, filepath.Join(other, ".env.local")), "has a private name (.env.*)"},
		{mkFile(t, filepath.Join(other, ".env.production")), "has a private name (.env.*)"},
		{mkFile(t, filepath.Join(other, ".env.example")), ""},
		{mkFile(t, filepath.Join(other, ".env.sample")), ""},
		{mkFile(t, filepath.Join(other, ".ENV.Template")), ""},
		{mkFile(t, filepath.Join(other, "shot.png")), ""},
		{mkFile(t, filepath.Join(other, ".envrc")), ""},
		{mkFile(t, filepath.Join(home, ".sshd", "notes.txt")), ""},
		{mkFile(t, filepath.Join(home, "Desktop", "shot.png")), ""},
	}
	// A link to a private file, a file in a folder that links to a private
	// folder, and C2's layout: a private folder that is itself a link.
	if symlink(t, key, filepath.Join(other, "link.png")) {
		dotfiles := filepath.Join(other, "dotfiles")
		mkFile(t, filepath.Join(dotfiles, "kube", "config"))
		_ = os.RemoveAll(filepath.Join(home, ".kube"))
		symlink(t, filepath.Join(dotfiles, "kube"), filepath.Join(home, ".kube"))
		symlink(t, filepath.Join(home, ".kube", "config"), filepath.Join(other, "drop.png"))
		symlink(t, filepath.Join(home, ".ssh"), filepath.Join(other, "keys"))
		cases = append(cases,
			struct{ path, why string }{filepath.Join(other, "link.png"), "is in ~/.ssh, a private folder"},
			struct{ path, why string }{filepath.Join(other, "keys", "config"), "is in ~/.ssh, a private folder"},
			struct{ path, why string }{filepath.Join(other, "drop.png"), "is in ~/.kube, a private folder"},
			struct{ path, why string }{filepath.Join(dotfiles, "kube", "config"), "is in ~/.kube, a private folder"})
	}
	// R5: other spellings of a private folder that open the same files: the
	// firmlink of the data volume on macOS, and a name that a file system
	// that ignores case folds, such as "ſ" for "s".
	for _, p := range []string{"/System/Volumes/Data" + key, filepath.Join(home, ".ſsh", "config"), filepath.Join(home, ".SSH", "config")} {
		if _, err := os.Stat(p); err == nil {
			cases = append(cases, struct{ path, why string }{p, "is in ~/.ssh, a private folder"})
		}
	}
	for _, c := range cases {
		f, why := openDropped(c.path)
		if why != c.why {
			t.Errorf("openDropped(%s) = %q, want %q", c.path, why, c.why)
		}
		if (f != nil) != (c.why == "") {
			t.Errorf("openDropped(%s) opened %v", c.path, f != nil)
		}
		if f != nil {
			_ = f.Close()
		}
	}
	for _, n := range []string{"credentialſ.json", "ID_RSA", "x.KEY", ".ENV"} {
		if privateName(n) == "" {
			t.Errorf("privateName(%q) is not private", n)
		}
	}
}

// The upload reads the file that the guard checked.
func TestDroppedFileIsTheCheckedOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not let a test rename a file that is open")
	}
	setHome(t)
	p := mkFile(t, filepath.Join(t.TempDir(), "shot.png"))
	f, why := openDropped(p)
	if why != "" {
		t.Fatal(why)
	}
	defer f.Close()
	// The path now names another file. The open one is still the checked one.
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	mkFile(t, p)
	if err := os.WriteFile(p, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, ok, err := readDropped(f, 1); err != nil || !ok || string(data) != "x" {
		t.Fatalf("read %q, %v, %v", data, ok, err)
	}
	if _, ok, _ := readDropped(must(os.Open(p)), 2); ok {
		t.Error("a file over the limit was taken")
	}
}

func must(f *os.File, err error) *os.File {
	if err != nil {
		panic(err)
	}
	return f
}

func TestPasteGuardNotices(t *testing.T) {
	home := setHome(t)
	m, _ := testMemory(t)
	g := newPasteGuard(m)
	key := mkFile(t, filepath.Join(home, ".ssh", "id_ed25519"))
	png := mkFile(t, filepath.Join(home, "Desktop", "shot.png"))
	if _, got := g.check([]byte(key), []string{key}); got != "did not upload ~/.ssh/id_ed25519: it is in ~/.ssh, a private folder. "+
		"The paste went in as text. Try: copy the file to the host yourself." {
		t.Errorf("one file: %q", got)
	}
	if _, got := g.check([]byte(png+" "+key), []string{png, key}); !strings.HasPrefix(got, "did not upload 2 files: ~/.ssh/id_ed25519 is in ~/.ssh") {
		t.Errorf("two files: %q", got)
	}
	d, got := g.check([]byte(png), []string{png})
	if got != "" || len(d.files) != 1 {
		t.Fatalf("a screenshot: %q", got)
	}
	d.close()
	g.observe([]byte(osc52(png)))
	if _, got := g.check([]byte(png), []string{png}); got != "did not upload ~/Desktop/shot.png: it is a path that a session put on your "+
		"clipboard in the last day. The paste went in as text. Try: copy the file to the host yourself." {
		t.Errorf("a path the session wrote: %q", got)
	}
	for size, want := range map[int]string{12: "uploaded a.png (12 bytes)", 1536: "uploaded a.png (1.5 KiB)", 3 << 20: "uploaded a.png (3.0 MiB)"} {
		if got := uploadNotice([]string{"a.png"}, []int{size}); got != want {
			t.Errorf("uploadNotice(%d) = %q, want %q", size, got, want)
		}
	}
	if got := uploadNotice([]string{"a.png", "b.png"}, []int{1, 2}); got != "uploaded a.png (1 bytes), b.png (2 bytes)" {
		t.Errorf("two files: %q", got)
	}
}

// V10: a drop computes its hashes once, and reads the memory once.
func TestPasteGuardCheckComputesOnce(t *testing.T) {
	setHome(t)
	m, _ := testMemory(t)
	g := newPasteGuard(m)
	dir := t.TempDir()
	paths := []string{mkFile(t, filepath.Join(dir, "a.png")), mkFile(t, filepath.Join(dir, "b.png")), mkFile(t, filepath.Join(dir, "c.png"))}
	sums, loads := keySums.Load(), m.loads.Load()
	d, why := g.check([]byte(strings.Join(paths, " ")), paths)
	if why != "" {
		t.Fatal(why)
	}
	d.close()
	if n := keySums.Load() - sums; n != int64(len(paths)+1) {
		t.Errorf("a drop of %d files computed %d hashes, want %d", len(paths), n, len(paths)+1)
	}
	if n := m.loads.Load() - loads; n != 1 {
		t.Errorf("a drop read the memory %d times, want 1", n)
	}
}

// W3: on Windows, the leaf changes into a link to a private file after the
// checks and before the open. The client checks the final path's name and
// the open file's identity. Unix opens the leaf with O_NOFOLLOW, which
// TestDropAncestorSwap checks.
//
// This test cannot fail on the final-name check alone. The final path's
// name differs from the checked name only when the leaf is a link or a
// reparse point at the open, and the leaf check (Lstat, the reparse point
// refusal and SameFile) refuses that first. A folder on the path that
// changes moves the leaf to another folder with the same name, which the
// folder check of the final path refuses. A Windows run is manual: the
// pull request lists it.
func TestDropLeafLinkWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows opens the leaf by path; unix opens it with O_NOFOLLOW, which TestDropAncestorSwap checks")
	}
	setHome(t)
	dir := t.TempDir()
	drop := mkFile(t, filepath.Join(dir, "shot.png"))
	secret := mkFile(t, filepath.Join(dir, ".env.local"))
	dropGap = func() {
		must0(t, os.Remove(drop))
		if err := os.Symlink(secret, drop); err != nil {
			t.Skip("this Windows does not let a test make a link")
		}
	}
	defer func() { dropGap = nil }()
	f, why := openDropped(drop)
	if f != nil {
		_ = f.Close()
	}
	if why == "" {
		t.Fatal("a leaf that became a link to .env.local was opened")
	}
}

// V3: a folder on the path changes after the checks and before the open.
// The open goes through folder handles, so it still catches it: a folder
// that became a link fails to open, and a folder that became a private one
// shows by its identity.
func TestDropAncestorSwap(t *testing.T) {
	home := setHome(t)
	mkFile(t, filepath.Join(home, ".ssh", "notes.txt"))
	other := t.TempDir()
	pub := filepath.Join(other, "pub")
	drop := mkFile(t, filepath.Join(pub, "notes.txt"))
	swaps := map[string]func(){
		// Windows and unix: the private folder itself moves into the path.
		"a private folder moved there": func() {
			must0(t, os.Rename(pub, pub+".old"))
			must0(t, os.Rename(filepath.Join(home, ".ssh"), pub))
		},
	}
	if runtime.GOOS != "windows" {
		swaps["a link to a private folder"] = func() {
			must0(t, os.Rename(pub, pub+".old"))
			must0(t, os.Symlink(filepath.Join(home, ".ssh"), pub))
		}
		swaps["a leaf that became a link"] = func() {
			must0(t, os.Rename(pub, pub+".old"))
			must0(t, os.MkdirAll(pub, 0o700))
			must0(t, os.Symlink(filepath.Join(home, ".ssh", "notes.txt"), filepath.Join(pub, "notes.txt")))
		}
	}
	for name, swap := range swaps {
		dropGap = swap
		f, why := openDropped(drop)
		dropGap = nil
		if f != nil {
			_ = f.Close()
		}
		if why == "" {
			t.Errorf("%s: the drop was opened", name)
		}
		// Put the folders back for the next case.
		_ = os.Remove(pub)
		if _, err := os.Stat(filepath.Join(home, ".ssh")); err != nil {
			must0(t, os.Rename(pub, filepath.Join(home, ".ssh")))
		}
		_ = os.RemoveAll(pub)
		must0(t, os.Rename(pub+".old", pub))
	}
}

func must0(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// testAttacher writes to a file in place of a terminal.
func testAttacher(t *testing.T) (*attacher, func() []byte) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	m, _ := testMemory(t)
	a := &attacher{o: AttachOptions{Out: out}, tracker: term.NewTracker(), guard: newPasteGuard(m)}
	return a, func() []byte {
		b, err := os.ReadFile(out.Name())
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
}

// The guard only reads: every byte reaches the terminal as it came, however
// the frames cut the clipboard writes.
func TestPasteGuardLeavesOutputUnchanged(t *testing.T) {
	setHome(t)
	a, terminal := testAttacher(t)
	var data []byte
	r := rand.New(rand.NewPCG(5, 6))
	forms := []string{osc52(victim), "\x1bPtmux;\x1b\x1b]52;c;" + b64(victim) + "\a\x1b\\", "\x1b]1337;Copy=:" + b64(victim) + "\x1b\\",
		"\x1b[\x9d52;c;" + b64(victim) + "\x9c", kittyWrite(7, 9), "\x1b\r]52;c;" + b64(victim) + "\x1b[0m"}
	for i := range 64 {
		for range 64 {
			data = append(data, byte(r.IntN(256)))
		}
		data = append(data, forms[i%len(forms)]...)
	}
	for off := 0; off < len(data); {
		n := min(1+r.IntN(97), len(data)-off)
		typ, payload := proto.TOutput, proto.EncodeOutput(int64(off), data[off:off+n])
		if r.IntN(8) == 0 {
			typ, payload = proto.TSnapshot, data[off:off+n]
			a.cu.Offset += int64(n)
		}
		if _, err := a.frame(proto.Frame{Type: typ, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		off += n
	}
	if got := terminal(); !bytes.Equal(got, data) {
		t.Fatalf("the terminal got %d bytes that differ from the session's %d", len(got), len(data))
	}
	if !remembersVictim(a.guard) {
		t.Error("the guard did not see the session's clipboard writes")
	}
}

func remembersVictim(g *pasteGuard) bool {
	why, _ := g.mem.check([][32]byte{must32(keySum(victim))}, nil)
	return why != ""
}

func must32(s [32]byte, _ bool) [32]byte { return s }

// R2: the client's own writes go through the guard too, such as a title
// that it sends again after a reconnect. A title cannot carry a second
// sequence any more, and a write in bytes of the client's is still seen.
func TestPasteGuardReadsClientWrites(t *testing.T) {
	setHome(t)
	a, _ := testAttacher(t)
	hidden := []byte("\x1b]2;title\x1b]52;c;" + b64("/tmp/other.pdf") + "\a")
	a.show(hidden)
	a.tracker.Feed(hidden)
	if bytes.Contains(a.tracker.M.Title, []byte("]52;")) {
		t.Fatalf("the title holds a clipboard write: %q", a.tracker.M.Title)
	}
	a.write([]byte(osc52(victim)))
	if !remembersVictim(a.guard) {
		t.Error("a clipboard write in the client's own bytes was not seen")
	}
}

// V8: a notice never cuts a character of three or four bytes that frames
// cut in three or four parts.
func TestNoticesWaitForWholeCharacters(t *testing.T) {
	setHome(t)
	for _, char := range []string{"€", "😀"} {
		a, terminal := testAttacher(t)
		a.o.Host.Name = "box"
		for i := range len(char) {
			a.show([]byte(char[i : i+1]))
			if i < len(char)-1 {
				a.notify("note")
			}
		}
		got := terminal()
		if !bytes.HasPrefix(got, []byte(char)) {
			t.Errorf("%q: a notice cut the character: %q", char, got)
		}
		if n := bytes.Count(got, []byte("note")); n != len(char)-1 {
			t.Errorf("%q: %d notices, want %d", char, n, len(char)-1)
		}
	}
}

// V9: while the output stays inside a sequence, notices of the user's own
// actions all wait, and only notices about other sessions are bounded.
// A notice of the user's own action goes through the text cleaner, as
// text from a host does, but is not cut, so it names every file of a drop.
func TestNoticeIsCleanedNotCut(t *testing.T) {
	setHome(t)
	a, terminal := testAttacher(t)
	a.o.Host.Name = "box"
	a.notify("uploaded \x1b]52;c;QUFB\a" + strings.Repeat("/a/long/path.png (3 bytes), ", 100) + "last.png (3 bytes)")
	got := string(terminal())
	if strings.Contains(got, "\x1b]52") || strings.Contains(got, "\a") {
		t.Errorf("the notice holds a control: %q", got)
	}
	if !strings.Contains(got, "last.png (3 bytes)") {
		t.Error("the notice was cut")
	}
}

func TestNoticesNeverDropUploads(t *testing.T) {
	setHome(t)
	a, terminal := testAttacher(t)
	a.o.Host.Name = "box"
	a.show([]byte("\x1b[38;2;1"))
	for i := range 20 {
		a.notify(fmt.Sprintf("uploaded f%d.png (1 bytes)", i))
		a.notice([]byte(fmt.Sprintf("\x1b]777;notify;t;other%d\x1b\\", i)), false)
	}
	a.show([]byte(";2;3m"))
	got := string(terminal())
	for i := range 20 {
		if !strings.Contains(got, fmt.Sprintf("uploaded f%d.png", i)) {
			t.Errorf("the notice of upload %d was dropped", i)
		}
	}
	if n := strings.Count(got, "other"); n != maxBackground {
		t.Errorf("%d notices about other sessions, want %d", n, maxBackground)
	}
}

// R9: a notice waits until the terminal is between sequences and
// characters, so it never cuts one of the session's.
func TestNoticesWaitForGround(t *testing.T) {
	setHome(t)
	a, terminal := testAttacher(t)
	a.o.Host.Name = "box"
	a.show([]byte("\x1b[38;2;1"))
	a.notify("first")
	a.show([]byte(";2;3mtext \xc3"))
	a.notify("second")
	a.show([]byte("\xa9 more"))
	got := terminal()
	want := "\x1b[38;2;1;2;3mtext \xc3\xa9 more"
	if !bytes.HasPrefix(got, []byte(want)) {
		t.Fatalf("the terminal got %q, want the session's bytes whole first", got)
	}
	if rest := string(got[len(want):]); !strings.Contains(rest, "first") || !strings.Contains(rest, "second") {
		t.Fatalf("the notices did not follow: %q", rest)
	}
	a.notify("third")
	if !bytes.HasSuffix(terminal(), []byte("third\x1b\\")) {
		t.Fatal("a notice in ground waited")
	}
}

// V1, round 5: the guard that an attach makes reads with both readings
// from the start, and records what each reads. The W1 sequence has no C1
// control, and only xterm's reading sees its write.
func TestPasteGuardReadsBothFromTheStart(t *testing.T) {
	setHome(t)
	m, _ := testMemory(t)
	g := newPasteGuard(m)
	if len(g.readers) != 2 || g.readers[0].xterm || !g.readers[1].xterm {
		t.Fatal("the guard does not start with Ghostty's reading and xterm's")
	}
	g.observe([]byte("\x1b\u00dd]52;c;" + b64(victim) + "\a"))
	if why, _ := m.check([][32]byte{sumOf(t, victim)}, nil); why == "" {
		t.Fatal("the xterm reading of ESC, a letter with 0x9D, and ] was not recorded")
	}
	// The next attach in the process keeps both readings.
	g.detach()
	if g2 := newPasteGuard(m); len(g2.readers) != 2 || !g2.readers[1].xterm || g2.readers[0].xterm {
		t.Fatal("the next attach lost the xterm reading")
	}
}

// V1, round 5: the attach's own frames, cut at every byte, carry the W1
// sequence to the guard.
func TestPasteGuardFramesCutAtEveryByte(t *testing.T) {
	setHome(t)
	data := []byte("\x1b\u00dd]52;c;" + b64(victim) + "\a")
	for i := 1; i < len(data); i++ {
		a, terminal := testAttacher(t)
		for k, part := range [][]byte{data[:i], data[i:]} {
			off := int64(0)
			if k == 1 {
				off = int64(i)
			}
			if _, err := a.frame(proto.Frame{Type: proto.TOutput, Payload: proto.EncodeOutput(off, part)}); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(terminal(), data) {
			t.Fatalf("cut at %d: the terminal got other bytes", i)
		}
		if !remembersVictim(a.guard) {
			t.Fatalf("cut at %d: the write was not recorded", i)
		}
	}
}

// Layer 1: when the clipboard cannot be read, a paste counts as a drag,
// so the hash check alone decides, and the attach notes it once.
func TestClipboardUnreadableFallsBack(t *testing.T) {
	t.Setenv(brand.Env("TEST_CLIPBOARD_TEXT"), filepath.Join(t.TempDir(), "none"))
	a := &attacher{}
	for range 2 {
		if a.pastedFromClipboard(t.Context(), []byte("/tmp/a.png")) {
			t.Fatal("an unreadable clipboard made a paste")
		}
	}
	if !a.clipUnread {
		t.Error("the attach did not note that it could not read the clipboard")
	}
	clip := filepath.Join(t.TempDir(), "clipboard")
	must0(t, os.WriteFile(clip, []byte("'/tmp/a b.png'\r\n"), 0o600))
	t.Setenv(brand.Env("TEST_CLIPBOARD_TEXT"), clip)
	if !a.pastedFromClipboard(t.Context(), []byte("/tmp/a\\ b.png")) {
		t.Error("a paste with other quotes and line ends than the clipboard's was not its text")
	}
}

func FuzzPasteGuard(f *testing.F) {
	for _, s := range []string{osc52(victim), "\x1b]5522;type=wdata;QUFB\x1b\\", "\x1b]1337;CopyToClipboard\a" + victim, "\x1b\x1b]52;;\x1b",
		"\x1b[\x9d52;c;QUFB\x9c", "\x1bP\x9d1337;Copy=:QUFB\a", kittyWrite(4, 1)} {
		f.Add([]byte(s), uint8(3))
	}
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		r := newTestReader()
		n := int(step%32) + 1
		for i := 0; i < len(data); i += n {
			r.observe(data[i:min(i+n, len(data))])
		}
		// One whole read gives the same readings, states and writes.
		w := newTestReader()
		w.observe(data)
		if len(w.readers) != len(r.readers) || len(w.got) != len(r.got) || len(w.sel) != len(r.sel) {
			t.Fatalf("in parts: %d readings, %d+%d writes; whole: %d readings, %d+%d writes",
				len(r.readers), len(r.got), len(r.sel), len(w.readers), len(w.got), len(w.sel))
		}
		for i := range r.readers {
			if r.readers[i].state != w.readers[i].state {
				t.Fatalf("reading %d: state %d in parts, %d whole", i, r.readers[i].state, w.readers[i].state)
			}
		}
		_ = r.inProgress()
	})
}
