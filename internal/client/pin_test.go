package client

import (
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Ask the real ssh which of the pin's options it knows now, before a test
// puts a fake ssh on PATH, which would answer for the whole binary.
func init() {
	if code, handled := GitSSHMode(os.Args[1:]); handled {
		os.Exit(code)
	}
	_, _ = optionalPinArgs()
	// A process that a test starts again to exit with a code of its choice,
	// on every system (exitState).
	if code := os.Getenv("PIN_TEST_EXIT"); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			n = 255
		}
		os.Exit(n)
	}
}

// newKey makes a host key of type typ with random bytes in it: the client
// only stores, compares and fingerprints keys, so it needs no real one.
func newKey(t testing.TB, typ string) hostKey {
	t.Helper()
	r := make([]byte, 32)
	if _, err := rand.Read(r); err != nil {
		t.Fatal(err)
	}
	b := binary.BigEndian.AppendUint32(nil, uint32(len(typ)))
	b = append(b, typ...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(r)))
	return hostKey{Type: typ, Blob: append(b, r...)}
}

// pinFor gives the host called name a pin of a new key, and saves it when
// hosts.toml lacks it, as every host that an unstaged connection reaches
// has (connArgs). The client folders must be the test's own (sshDirs).
func pinFor(t testing.TB, name string) {
	t.Helper()
	if os.Getenv("XDG_CONFIG_HOME") == "" {
		t.Fatal("pinFor needs the test's own config folder")
	}
	if !staging(name) {
		if ok, err := savedHost(name); err != nil {
			t.Fatal(err)
		} else if !ok {
			if err := SaveHost(Host{Name: name, SSH: name}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writeTestPin(name, []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
}

// authorized is k as a known hosts line names it, without the host.
func authorized(k hostKey) string { return strings.TrimPrefix(k.line(""), " ") }

// trustLog is what ssh logs on the connection of trustHostKey, for a host
// that shows k, with OpenSSH's verdict: "trusted", "unknown", "changed" or
// "revoked". ssh ends each line with \r\n on stderr.
func trustLog(k hostKey, verdict string) string {
	lines := []string{
		"debug1: Connecting to box.example [192.0.2.7] port 22.",
		"debug1: Remote protocol version 2.0, remote software version debug1: Authentications that can continue: x",
		"debug1: Server host key: " + k.Type + " " + k.Fingerprint(),
	}
	switch verdict {
	case "trusted":
		lines = append(lines, "debug1: Host 'box.example' is known and matches the "+keyFamily(k.Type)+" host key.",
			"debug1: SSH2_MSG_SERVICE_ACCEPT received", "debug1: Authentications that can continue: publickey",
			"me@box.example: Permission denied (publickey).")
	case "unknown":
		lines = append(lines, "No "+keyFamily(k.Type)+" host key is known for box.example and you have requested strict checking.", "Host key verification failed.")
	case "changed":
		lines = append(lines, "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@",
			"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @", "Host key verification failed.")
	case "revoked":
		lines = append(lines, "Host key "+k.Type+" "+k.Fingerprint()+" revoked by file /etc/ssh/revoked", "Host key verification failed.")
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

// fakeSSH puts a fake ssh first on PATH, and returns its folder. It logs
// each call's arguments to log, one call to a line.
//   - ssh -G prints the file values.
//   - The connection of trustHostKey (LogLevel=DEBUG1) writes trust-log to
//     the file that -E names, or trust-log-<algorithm> with
//     HostKeyAlgorithms when there is one, and prints hop-stderr, as a
//     ProxyJump hop would.
//   - A connection that reads a key (PreferredAuthentications=none) writes
//     the first line of the file shows, with HostKeyAlgorithms the first of
//     its type, under the alias it was given, to the file that
//     UserKnownHostsFile names, prints fetch-stderr, then fails the login.
//   - Every other call prints the file stderr and exits with 255.
func fakeSSH(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	sshDirs(t)
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "$FAKE_SSH/log"
case " $* " in
*" -G "*) cat "$FAKE_SSH/values" 2>/dev/null; exit 0;;
*" -O "*) exit 0;;
*" LogLevel=DEBUG1 "*)
  log=/dev/stderr; algs=; prev=
  for a; do
    [ "$prev" = -E ] && log=$a
    case $a in HostKeyAlgorithms=*) algs=${a#HostKeyAlgorithms=};; esac
    prev=$a
  done
  tl="$FAKE_SSH/trust-log"
  [ -n "$algs" ] && [ -f "$FAKE_SSH/trust-log-${algs%%,*}" ] && tl="$FAKE_SSH/trust-log-${algs%%,*}"
  [ -f "$FAKE_SSH/slow" ] && sleep 1
  [ -f "$FAKE_SSH/hop-stderr" ] && cat "$FAKE_SSH/hop-stderr" >&2
  cat "$tl" >> "$log" 2>/dev/null
  exit 255;;
*" PreferredAuthentications=none "*)
  algs=
  for a; do case $a in UserKnownHostsFile=*) f=${a#UserKnownHostsFile=};; HostKeyAlias=*) alias=${a#HostKeyAlias=};; HostKeyAlgorithms=*) algs=${a#HostKeyAlgorithms=};; esac; done
  [ -f "$FAKE_SSH/slow" ] && sleep 1
  if [ -f "$FAKE_SSH/shows" ]; then
    while read -r typ key; do
      if [ -z "$algs" ]; then echo "$alias $typ $key" >> "$f"; break; fi
      case ",$algs," in *",$typ,"*) echo "$alias $typ $key" >> "$f"; break;; esac
    done < "$FAKE_SSH/shows"
  fi
  [ -f "$FAKE_SSH/fetch-hold" ] && touch "$FAKE_SSH/fetch-wrote" && sleep 2
  [ -f "$FAKE_SSH/fetch-stderr" ] && cat "$FAKE_SSH/fetch-stderr" >&2
  echo "Permission denied (publickey)." >&2
  exit 255;;
esac
log=/dev/stderr; prev=
for a; do [ "$prev" = -E ] && log=$a; prev=$a; done
[ -f "$FAKE_SSH/hop-stderr" ] && cat "$FAKE_SSH/hop-stderr" >&2
[ -f "$FAKE_SSH/stderr" ] && cat "$FAKE_SSH/stderr" >> "$log"
[ -f "$FAKE_SSH/hang" ] && exec sleep 10
[ -f "$FAKE_SSH/kill-self" ] && kill -9 $$
[ -f "$FAKE_SSH/exit-code" ] && exit "$(cat "$FAKE_SSH/exit-code")"
exit 255
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SSH", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// put writes a file of the fake ssh.
func put(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// shows makes the fake host show keys, the first one by default.
func shows(t *testing.T, dir string, keys ...hostKey) {
	t.Helper()
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(authorized(k) + "\n")
	}
	put(t, dir, "shows", b.String())
}

// sshCalls are the fake ssh's calls, each as one line of its arguments.
func sshCalls(t *testing.T, dir string) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	return slices.DeleteFunc(strings.Split(strings.TrimSpace(string(b)), "\n"), func(l string) bool { return l == "" || strings.HasSuffix(l, "-- probe.invalid") })
}

// fetches counts the connections that read a host key.
func fetches(t *testing.T, dir string) int {
	n := 0
	for _, c := range sshCalls(t, dir) {
		if hasOption(c, "PreferredAuthentications=none") && !hasOption(c, "LogLevel=DEBUG1") {
			n++
		}
	}
	return n
}

// hasOption reports whether the call, as the fake logs it, has -o opt.
func hasOption(call, opt string) bool { return strings.Contains(" "+call+" ", " -o "+opt+" ") }

// values are what the fake's ssh -G prints for a host with this
// StrictHostKeyChecking.
func values(strict string) string {
	return "hostname box.example\nport 22\nstricthostkeychecking " + strict + "\n"
}

func TestParseKeyLine(t *testing.T) {
	k := newKey(t, "ssh-ed25519")
	for _, c := range []struct {
		line   string
		marker string
		ok     bool
	}{
		{"box " + authorized(k), "", true},
		{"box,192.0.2.7 " + authorized(k) + " a comment", "", true},
		{"@revoked * " + authorized(k), "revoked", true},
		{"# box " + authorized(k), "", false},
		{"", "", false},
		{"box ssh-rsa " + strings.Fields(authorized(k))[1], "", false}, // the wire form says ed25519
		{"box ssh-ed25519 !!!", "", false},
		{"box ssh-ed25519-cert-v01@openssh.com " + strings.Fields(authorized(k))[1], "", false},
	} {
		marker, got, ok := parseKeyLine(c.line)
		if ok != c.ok || marker != c.marker || (ok && got.Fingerprint() != k.Fingerprint()) {
			t.Errorf("parseKeyLine(%q) = %q, %v, %v", c.line, marker, got.Fingerprint(), ok)
		}
	}
	if got := readKeys([]byte("a " + authorized(k) + "\nb " + authorized(k) + "\n")); len(got) != 1 {
		t.Errorf("readKeys kept %d copies of one key", len(got))
	}
}

// ssh reads a backslash in an -o value as an escape, and a space as the end
// of a file.
func TestSSHPathArg(t *testing.T) {
	for _, c := range []struct{ path, system, want string }{
		{"/home/me/.config/x/ssh/known_hosts-box", "linux", "/home/me/.config/x/ssh/known_hosts-box"},
		{"/Users/me/My Files/.config/x/ssh/known_hosts-box", "darwin", `"/Users/me/My Files/.config/x/ssh/known_hosts-box"`},
		{`C:\Users\me\.config\x\ssh\known_hosts-box`, "windows", "C:/Users/me/.config/x/ssh/known_hosts-box"},
		{`C:\Users\me\My Files\kh`, "windows", `"C:/Users/me/My Files/kh"`},
	} {
		if got := sshPathArg(c.path, c.system); got != c.want {
			t.Errorf("sshPathArg(%q, %s) = %q, want %q", c.path, c.system, got, c.want)
		}
	}
}

// The pin file: 0600, the host's keys under the alias, and nothing for a
// name that cannot name a host.
func TestPinFile(t *testing.T) {
	sshDirs(t)
	k := newKey(t, "ssh-ed25519")
	if err := writeTestPin("box", []hostKey{k}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(ConfigDir(), "ssh", "known_hosts-"+HostFileKey("box"))
	if p != pinPath("box") || !regexp.MustCompile(`^known_hosts-box-[0-9a-f]{32}$`).MatchString(filepath.Base(p)) {
		t.Errorf("pinPath(box) = %s", pinPath("box"))
	}
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "\n"+k.line(brand.Name+"-box")+"\n") || !strings.HasPrefix(string(b), "# ") {
		t.Fatalf("pin = %q, %v", b, err)
	}
	if st, err := os.Stat(p); runtime.GOOS != "windows" && (err != nil || st.Mode().Perm() != 0o600) {
		t.Errorf("pin mode = %v, %v", st.Mode(), err)
	}
	if !HasPin("box") || HasPin("box2") {
		t.Error("HasPin")
	}
	if pin, err := PinOf("box"); err != nil || pin.Fingerprint != k.Fingerprint() || pin.Source != PinKept {
		t.Errorf("PinOf = %+v, %v", pin, err)
	}
	for _, bad := range []string{"../box", "a/b", `a\b`, "", "-x", "..", "box.", "box ", "box\t"} {
		if HasPin(bad) || pinPath(bad) != "" || writeTestPin(bad, []hostKey{k}) == nil || forgetPin(bad) != nil {
			t.Errorf("a pin for %q", bad)
		}
	}
	// A pin that holds no key: HasPin still says yes, so ssh refuses.
	put(t, filepath.Dir(p), filepath.Base(pinPath("empty")), "# nothing\n")
	if !HasPin("empty") {
		t.Error("an empty pin is no pin")
	}
	if _, err := readPin("empty"); err == nil {
		t.Error("an empty pin has keys")
	}
}

// The line that host add prints, and the command that checks the key.
func TestPinLine(t *testing.T) {
	fp := "SHA256:" + strings.Repeat("a", 43)
	for _, c := range []struct {
		p    Pin
		line string
	}{
		{Pin{Fingerprint: fp, Keys: 1, Source: PinFromKnownHosts}, "host key " + fp + " pinned, from your known_hosts"},
		{Pin{Fingerprint: fp, Keys: 2, Source: PinMatchedFlag}, "host key " + fp + " (and 1 more) pinned, matched --host-key"},
		{Pin{Fingerprint: fp, Keys: 1, Source: PinFirstUse}, "host key " + fp + " pinned on first use"},
		{Pin{Fingerprint: fp, Keys: 1, Source: PinKept}, "host key " + fp + " already pinned"},
	} {
		if got := c.p.Line(); got != c.line {
			t.Errorf("Line = %q, want %q", got, c.line)
		}
	}
	for typ, want := range map[string]string{"ssh-ed25519": "ed25519", "ecdsa-sha2-nistp256": "ecdsa", "ssh-rsa": "rsa"} {
		if got := (Pin{Type: typ}).CheckCommand(); got != "ssh-keygen -lf /etc/ssh/ssh_host_"+want+"_key.pub" {
			t.Errorf("CheckCommand(%s) = %q", typ, got)
		}
	}
	if checkFingerprint(fp) != nil || checkFingerprint("SHA256:short") == nil || checkFingerprint("MD5:aa:bb") == nil {
		t.Error("checkFingerprint")
	}
	for typ, want := range map[string]string{"ssh-ed25519": "ssh-ed25519", "ssh-ed25519-cert-v01@openssh.com": "ssh-ed25519",
		"ssh-rsa": "rsa-sha2-512,rsa-sha2-256,ssh-rsa", "ecdsa-sha2-nistp384": "ecdsa-sha2-nistp384"} {
		if got := algorithmsFor(typ); got != want {
			t.Errorf("algorithmsFor(%s) = %q", typ, got)
		}
	}
}

// Option detection fails closed: only ssh's own words say that it does not
// know an option. A slow or failed answer is an error, which is not kept,
// so it can never drop KnownHostsCommand=none from a command line.
func TestOptionDetectionFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	knowsMu.Lock()
	kept := knows
	knows = map[string]bool{}
	knowsMu.Unlock()
	t.Cleanup(func() { knowsMu.Lock(); knows = kept; knowsMu.Unlock() })
	// A slow ssh gets a short wait, and a quick one the usual wait, which a
	// busy machine needs.
	wait := sshGWait
	t.Cleanup(func() { sshGWait = wait })
	answer := func(body string) {
		sshGWait = wait
		if strings.HasPrefix(body, "sleep") {
			sshGWait = 2 * time.Second
		}
		if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	answer("echo 'command-line: line 0: Bad configuration option: knownhostscommand' >&2; exit 255\n")
	if ok, err := knowsOption("KnownHostsCommand=none"); ok || err != nil {
		t.Errorf("an old ssh: %v, %v", ok, err)
	}
	answer("echo 'Unsupported option \"gssapiauthentication\"' >&2; exit 0\n")
	if ok, err := knowsOption("GSSAPIAuthentication=no"); ok || err != nil {
		t.Errorf("an option this build leaves out: %v, %v", ok, err)
	}
	answer("sleep 30\n")
	start := time.Now()
	if ok, err := knowsOption("NoHostAuthenticationForProxyCommand=no"); ok || err == nil || time.Since(start) > 15*time.Second {
		t.Errorf("a slow ssh: %v, %v", ok, err)
	}
	answer("exit 0\n")
	if ok, err := knowsOption("NoHostAuthenticationForProxyCommand=no"); !ok || err != nil {
		t.Errorf("after a slow answer, it asks again: %v, %v", ok, err)
	}
	// While ssh cannot tell, a pinned host gets no command line at all.
	sshDirs(t)
	answer("sleep 30\n")
	knowsMu.Lock()
	knows = map[string]bool{}
	knowsMu.Unlock()
	if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&SSH{Host: Host{Name: "box", SSH: "box.example"}}).baseArgs(); err == nil {
		t.Error("a command line without KnownHostsCommand=none")
	}
}

// Hosts with different pins never share a shared connection: the socket's
// name holds the host's name and its pin.
func TestControlKey(t *testing.T) {
	sshDirs(t)
	path := func(name string) string {
		t.Helper()
		args, err := (&SSH{Host: Host{Name: name, SSH: "box.example"}}).baseArgs()
		if err != nil {
			t.Fatal(err)
		}
		return sharedPath(args)
	}
	if !controlMaster {
		t.Skip("the client shares no SSH connections here")
	}
	unpinned := path("box")
	a, b := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	if err := writeTestPin("box", []hostKey{a}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin("alias", []hostKey{b}); err != nil {
		t.Fatal(err)
	}
	pinnedA, alias := path("box"), path("alias")
	if pinnedA == unpinned || pinnedA == alias || !strings.HasSuffix(pinnedA, "-%C") ||
		!strings.HasPrefix(filepath.Base(pinnedA), "nf-") || filepath.Base(pinnedA) != controlName("") {
		t.Errorf("sockets: unpinned %s, box %s, alias %s", unpinned, pinnedA, alias)
	}
	if err := writeTestPin("box", []hostKey{b}); err != nil { // a rekey
		t.Fatal(err)
	}
	if after := path("box"); after == pinnedA || after == alias {
		t.Errorf("after a rekey, box's socket is %s, before %s, alias's %s", after, pinnedA, alias)
	}
}

// ForgetHost removes the host's pin, and no other file. A saved name that
// cannot name a host leaves hosts.toml, and no file is touched.
