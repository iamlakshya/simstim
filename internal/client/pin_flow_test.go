package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The options that every connection that reads a host key must have: it
// offers no key and no password, uses no agent and no shared connection,
// and reads no other source of keys.
var wantFetch = []string{
	"PreferredAuthentications=none", "PubkeyAuthentication=no", "PasswordAuthentication=no",
	"KbdInteractiveAuthentication=no", "IdentityAgent=none", "ControlMaster=no", "ControlPath=none",
	"GlobalKnownHostsFile=" + os.DevNull, "VerifyHostKeyDNS=no", "UpdateHostKeys=no", "HashKnownHosts=no",
	"NoHostAuthenticationForLocalhost=no", "LogLevel=ERROR", "ForwardAgent=no",
}

// The options of the connection that asks the user's settings: no login,
// no question, no write to a known hosts file, a debug log in SHA256.
var wantTrust = []string{
	"PreferredAuthentications=none", "PubkeyAuthentication=no", "PasswordAuthentication=no",
	"KbdInteractiveAuthentication=no", "IdentityAgent=none", "ControlMaster=no", "ControlPath=none",
	"StrictHostKeyChecking=yes", "BatchMode=yes", "UpdateHostKeys=no", "CheckHostIP=no",
	"NoHostAuthenticationForLocalhost=no", "FingerprintHash=sha256", "LogLevel=DEBUG1", "ForwardAgent=no",
}

// calls returns the fake's calls of one kind: "trust", "fetch" or "G".
func calls(t *testing.T, dir, kind string) []string {
	var out []string
	for _, c := range sshCalls(t, dir) {
		trust := hasOption(c, "LogLevel=DEBUG1")
		switch {
		case kind == "trust" && trust, kind == "fetch" && !trust && hasOption(c, "PreferredAuthentications=none"),
			kind == "G" && strings.Contains(c, " -G "):
			out = append(out, c)
		}
	}
	return out
}

// PinHostKey: OpenSSH's verdict on the user's settings, --host-key, and the
// first use.
func TestPinHostKey(t *testing.T) {
	ctx := context.Background()
	h := Host{Name: "box", SSH: "box.example"}
	k, other, rsa := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519"), newKey(t, "ssh-rsa")
	p256, p384, p521 := newKey(t, "ecdsa-sha2-nistp256"), newKey(t, "ecdsa-sha2-nistp384"), newKey(t, "ecdsa-sha2-nistp521")
	for _, c := range []struct {
		name      string
		verdict   string // on the key that the trust connection sees
		seen      hostKey
		shows     []hostKey
		revoked   *hostKey // a key of another type that the user's settings revoke
		swapped   bool     // a connection of another type shows another key than the fetch
		want      string
		firstUse  FirstUse
		pinned    hostKey
		src       PinSource
		fetches   int
		errCode   string
		unchecked bool
	}{
		{name: "trusted", verdict: "trusted", seen: k, shows: []hostKey{k}, pinned: k, src: PinFromKnownHosts, fetches: 1},
		{name: "trusted, with no question", verdict: "trusted", seen: k, shows: []hostKey{k}, firstUse: FirstUseRefuse, pinned: k, src: PinFromKnownHosts, fetches: 1},
		{name: "trusted RSA, the host shows Ed25519 first", verdict: "trusted", seen: rsa, shows: []hostKey{k, rsa}, pinned: rsa, src: PinFromKnownHosts, fetches: 1},
		{name: "trusted, then another key", verdict: "trusted", seen: k, shows: []hostKey{other}, fetches: 1, errCode: "host_key_mismatch"},
		{name: "--host-key that the user's settings trust", verdict: "trusted", seen: k, shows: []hostKey{k}, want: k.Fingerprint(), pinned: k, src: PinMatchedFlag, fetches: 1},
		{name: "--host-key that differs from known_hosts, and the host shows it", verdict: "changed", seen: k, shows: []hostKey{k}, want: k.Fingerprint(), pinned: k, src: PinMatchedFlag, fetches: 1},
		{name: "--host-key of another type", verdict: "unknown", seen: k, shows: []hostKey{k, rsa}, want: rsa.Fingerprint(), pinned: rsa, src: PinMatchedFlag, fetches: 5},
		{name: "--host-key that the host does not show", verdict: "unknown", seen: k, shows: []hostKey{k}, want: other.Fingerprint(), fetches: 5, errCode: "host_key_mismatch"},
		{name: "--host-key of P-384, the host shows P-256 first", verdict: "unknown", seen: p256, shows: []hostKey{p256, p384}, want: p384.Fingerprint(), pinned: p384, src: PinMatchedFlag, fetches: 3},
		{name: "--host-key of P-521, the host shows P-256 first", verdict: "unknown", seen: p256, shows: []hostKey{p256, p521}, want: p521.Fingerprint(), pinned: p521, src: PinMatchedFlag, fetches: 4},
		{name: "--host-key of a revoked key, the host shows a trusted one first", verdict: "trusted", seen: k, shows: []hostKey{k, rsa}, revoked: &rsa, want: rsa.Fingerprint(), fetches: 5, errCode: "host_key_revoked"},
		{name: "--host-key of a revoked key, known_hosts holds another", verdict: "changed", seen: k, shows: []hostKey{k, p384}, revoked: &p384, want: p384.Fingerprint(), fetches: 3, errCode: "host_key_revoked"},
		{name: "--host-key of a key that ssh does not show on its own connection", verdict: "unknown", seen: k, shows: []hostKey{k, rsa}, swapped: true, want: rsa.Fingerprint(), fetches: 5, errCode: "host_key_mismatch"},
		{name: "stale known_hosts", verdict: "changed", seen: k, shows: []hostKey{k}, firstUse: FirstUseAccept, errCode: "host_key_changed"},
		{name: "revoked", verdict: "revoked", seen: k, shows: []hostKey{k}, firstUse: FirstUseAccept, errCode: "host_key_revoked"},
		{name: "revoked, with --host-key", verdict: "revoked", seen: k, shows: []hostKey{k}, want: k.Fingerprint(), errCode: "host_key_revoked"},
		{name: "first use", verdict: "unknown", seen: k, shows: []hostKey{k}, firstUse: FirstUseAccept, pinned: k, src: PinFirstUse, fetches: 1},
		{name: "input off", verdict: "unknown", seen: k, shows: []hostKey{k}, firstUse: FirstUseRefuse, unchecked: true},
		{name: "a log it cannot read", verdict: "", seen: k, shows: []hostKey{k}, firstUse: FirstUseAccept, errCode: "unreachable"},
		{name: "no key shown", verdict: "unknown", seen: k, firstUse: FirstUseAccept, fetches: 1, errCode: "unreachable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := fakeSSH(t)
			put(t, dir, "trust-log", trustLog(c.seen, c.verdict))
			if c.verdict == "" {
				put(t, dir, "trust-log", "debug1: Server host key: ssh-ed25519 "+k.Fingerprint()+"\r\nssh: connect to host box.example port 22: Connection refused\r\n")
			}
			if len(c.shows) > 0 {
				shows(t, dir, c.shows...)
			}
			// What the user's settings say of each other key, on a
			// connection of its type.
			for _, o := range c.shows {
				log := trustLog(o, "unknown")
				switch {
				case c.revoked != nil && o.Fingerprint() == c.revoked.Fingerprint():
					log = trustLog(o, "revoked")
				case c.swapped:
					log = trustLog(other, "unknown")
				}
				if o.Fingerprint() != c.seen.Fingerprint() {
					put(t, dir, "trust-log-"+strings.Split(algorithmsFor(o.Type), ",")[0], log)
				}
			}
			if controlMaster {
				args, setupErr := (&SSH{Host: h}).baseArgs()
				if setupErr != nil {
					t.Fatal(setupErr)
				}
				previousSocket := expandedTestSocket(t, args)
				if err := os.WriteFile(previousSocket, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			pin, err := PinHostKey(ctx, h, c.want, c.firstUse)
			var ue *UserError
			switch {
			case c.unchecked:
				var uk *UncheckedKeyError
				if !errors.Is(err, ErrUnchecked) || !errors.As(err, &uk) || uk.Pin.Fingerprint != c.seen.Fingerprint() || uk.Pin.Type != c.seen.Type {
					t.Fatalf("err = %v, want ErrUnchecked with the key that the host showed", err)
				}
			case c.errCode != "":
				if !errors.As(err, &ue) || ue.Code != c.errCode || ue.Hint == "" {
					t.Fatalf("err = %#v, want %s", err, c.errCode)
				}
			case err != nil:
				t.Fatal(err)
			default:
				keys, rerr := readPin("box")
				if rerr != nil || len(keys) != 1 || keys[0].Fingerprint() != c.pinned.Fingerprint() || pin.Source != c.src || pin.Fingerprint != c.pinned.Fingerprint() {
					t.Fatalf("pin %+v, keys %v, %v", pin, keys, rerr)
				}
				// The shared connection of before stops.
				all := sshCalls(t, dir)
				if last := all[len(all)-1]; controlMaster && !strings.Contains(last, " -O exit -- placeholder") {
					t.Errorf("the master was not stopped after the pin: %q", all)
				}
			}
			if got := len(calls(t, dir, "fetch")); got != c.fetches {
				t.Errorf("%d connections read the key, want %d: %q", got, c.fetches, calls(t, dir, "fetch"))
			}
			if err != nil && HasPin("box") {
				t.Error("a failure left a pin")
			}
			for _, call := range calls(t, dir, "trust") {
				for _, o := range wantTrust {
					if !hasOption(call, o) {
						t.Errorf("the connection that asks the user's settings lacks -o %s: %q", o, call)
					}
				}
				if strings.Contains(call, "HostKeyAlias=") || strings.Contains(call, "UserKnownHostsFile=") || !strings.HasSuffix(call, " -T -- box.example true") {
					t.Errorf("the connection that asks the user's settings changes them: %q", call)
				}
			}
			for _, call := range calls(t, dir, "fetch") {
				for _, o := range append(wantFetch, "StrictHostKeyChecking=accept-new", "BatchMode=yes", "HostKeyAlias="+brand.Name+"-box") {
					if !hasOption(call, o) {
						t.Errorf("the connection that reads the key lacks -o %s: %q", o, call)
					}
				}
			}
		})
	}
}

// At a terminal, ssh asks: its question needs BatchMode off. A no pins
// nothing, and is a cancel.
func TestPinHostKeyAsks(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	put(t, dir, "fetch-stderr", "Host key verification failed.\n")
	_, err := PinHostKey(context.Background(), Host{Name: "box", SSH: "box.example"}, "", FirstUseAsk)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Kind != KindCancelled || HasPin("box") {
		t.Fatalf("a no: %#v", err)
	}
	call := calls(t, dir, "fetch")[0]
	if !hasOption(call, "StrictHostKeyChecking=ask") || !hasOption(call, "BatchMode=no") || !hasOption(call, "PreferredAuthentications=none") {
		t.Errorf("the connection that asks: %q", call)
	}
	shows(t, dir, k)
	if pin, err := PinHostKey(context.Background(), Host{Name: "box", SSH: "box.example"}, "", FirstUseAsk); err != nil || pin.Source != PinFirstUse {
		t.Errorf("a yes: %+v, %v", pin, err)
	}
}

// What the host writes reaches the user only cleaned.
func TestPinHostKeyCleansTheHostsWords(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	put(t, dir, "fetch-stderr", "banner \x1b]52;c;aGk=\x07 from the host\n")
	_, err := PinHostKey(context.Background(), Host{Name: "box", SSH: "box.example"}, "", FirstUseAccept)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("err = %q", err)
	}
	put(t, dir, "trust-log", "kex_exchange_identification: banner line 0: \x1b]52;c;aGk=\x07\r\n")
	_, err = PinHostKey(context.Background(), Host{Name: "box", SSH: "box.example"}, "", FirstUseAccept)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("err = %q", err)
	}
}

// The trust log: the first line that decides wins, and what the host sends
// before the key exchange, which ssh logs after words of its own, decides
// nothing.
func TestReadTrust(t *testing.T) {
	k := newKey(t, "ssh-ed25519")
	denied := "me@box.example: Permission denied (publickey).\r\n"
	// cut is the trusted log without its last line: ssh ended early.
	cut := strings.TrimSuffix(trustLog(k, "trusted"), denied)
	for _, c := range []struct {
		name    string
		log     string
		code    int // ssh's exit code
		kind    trustKind
		decided bool
	}{
		{"trusted", trustLog(k, "trusted"), 255, trusted, true},
		{"trusted, and logged in", cut + "Authenticated to box.example ([192.0.2.7]:22) using \"none\".\r\n", 0, trusted, true},
		{"unknown", trustLog(k, "unknown"), 255, trustUnknown, true},
		{"changed", trustLog(k, "changed"), 255, trustChanged, true},
		{"revoked", trustLog(k, "revoked"), 255, trustRevoked, true},
		{"a host's version that imitates the login", "debug1: Remote protocol version 2.0, remote software version debug1: Authentications that can continue: x\r\nHost key verification failed.\r\n", 255, trustUnknown, false},
		{"a login before any key", "debug1: Authentications that can continue: publickey\r\n", 255, trustUnknown, false},
		{"no verdict", "ssh: connect to host box port 22: Connection refused\r\n", 255, trustUnknown, false},
		// A line of the host's own, after the login, can only make the
		// verdict stricter.
		{"a failure line after the login", trustLog(k, "trusted") + "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n", 255, trustChanged, true},
		{"a certificate", "debug1: Server host certificate: ssh-ed25519-cert-v01@openssh.com " + k.Fingerprint() +
			`, serial 0 ID "debug1: Server host key: x" CA ssh-ed25519 SHA256:` + strings.Repeat("c", 43) + " valid forever\r\n" +
			"debug1: SSH2_MSG_SERVICE_ACCEPT received\r\n" + denied, 255, trusted, true},
		// A certificate's key ID with a line break and a login line, before
		// ssh's own verdict: the verdict wins.
		{"a key ID that imitates the login, on a changed key", injected(k, "changed"), 255, trustChanged, true},
		{"a key ID that imitates the login, on an unknown key", injected(k, "unknown"), 255, trustUnknown, true},
		{"a key ID that imitates the login, on a revoked key", injected(k, "revoked"), 255, trustRevoked, true},
		{"a key ID that imitates not known, on a changed key", strings.Replace(injected(k, "changed"), "debug1: Authentications that can continue: publickey",
			"No ED25519 host key is known for box and you have requested strict checking.", 1), 255, trustChanged, true},
		{"a key ID that imitates the login, then a failure", strings.Replace(injected(k, "trusted"), "debug1: Host 'box.example' is known", "Host key verification failed.\r\ndebug1: Host", 1), 255, trustUnknown, false},
		{"a key ID that names a second key", strings.Replace(trustLog(k, "trusted"), "debug1: Server host key: ", "debug1: Server host key: ssh-ed25519 SHA256:"+strings.Repeat("d", 43)+"\r\ndebug1: Server host key: ", 1), 255, trustUnknown, false},
		// The check ends on a failure of ssh's own, which no pattern names:
		// what the key ID imitated, the login and the refusal, never counts.
		{"a key ID that imitates the refusal, then ssh fails", injectedEnd(k, "ssh: KnownHostsCommand failed\r\n"), 255, trustUnknown, false},
		{"a key ID that imitates not known, then ssh fails", strings.Replace(injectedEnd(k, "ssh: KnownHostsCommand failed\r\n"), "debug1: Authentications that can continue: publickey",
			"No ED25519 host key is known for box and you have requested strict checking.", 1), 255, trustUnknown, false},
		{"a log that ends early", cut, 255, trustUnknown, false},
		{"a trusted log from an ssh that a signal ended", trustLog(k, "trusted"), -1, trustUnknown, false},
		{"a trusted log, and an exit code of another kind", trustLog(k, "trusted"), 1, trustUnknown, false},
	} {
		got, why := readTrust(c.log, c.code)
		if ok := why == ""; ok != c.decided || (ok && got.kind != c.kind) || (ok && got.fp != k.Fingerprint()) {
			t.Errorf("%s: %+v, %q", c.name, got, why)
		}
	}
}

// injectedEnd is a log whose certificate's key ID imitates the login and
// the host's refusal of it, and that ends with end, a line of ssh's own.
func injectedEnd(k hostKey, end string) string {
	return "debug1: Connecting to box.example [192.0.2.7] port 22.\r\n" +
		"debug1: Server host certificate: ssh-ed25519-cert-v01@openssh.com " + k.Fingerprint() +
		`, serial 0 ID "x" CA ssh-ed25519 SHA256:fake valid forever` + "\r\n" +
		"debug1: Authentications that can continue: publickey\r\n" +
		"me@box.example: Permission denied (publickey).\r\n" +
		`tail" CA ssh-ed25519 SHA256:` + strings.Repeat("c", 43) + " valid forever\r\n" + end
}

// injected is trustLog with a certificate whose key ID holds a line break
// and a login line, as ssh logs it before its verdict.
func injected(k hostKey, verdict string) string {
	cert := "debug1: Server host certificate: ssh-ed25519-cert-v01@openssh.com " + k.Fingerprint() +
		`, serial 0 ID "x" CA ssh-ed25519 SHA256:fake valid forever` + "\r\n" +
		"debug1: Authentications that can continue: publickey\r\n" +
		`tail" CA ssh-ed25519 SHA256:` + strings.Repeat("c", 43) + " valid forever\r\n"
	return strings.Replace(trustLog(k, verdict), "debug1: Server host key: "+k.Type+" "+k.Fingerprint()+"\r\n", cert, 1)
}

// The pin's options are there exactly when the host has a pin, before the
// destination, once each, and they win over every config file, because ssh
// keeps the first value of each.
func TestBaseArgsHaveThePin(t *testing.T) {
	sshDirs(t)
	s := &SSH{Host: Host{Name: "box", SSH: "dev@box"}}
	pinOpts := []string{"HostKeyAlias=", "UserKnownHostsFile=", "StrictHostKeyChecking=", "UpdateHostKeys=", "GlobalKnownHostsFile=",
		"VerifyHostKeyDNS=", "NoHostAuthenticationForLocalhost=", "CheckHostIP=", "FingerprintHash=", "LogLevel="}
	args, err := s.baseArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		for _, o := range pinOpts {
			if strings.HasPrefix(a, o) {
				t.Errorf("an unpinned host has %s", a)
			}
		}
	}
	if err := SaveHosts(context.Background(), []Host{s.Host}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cmd, err := s.Command(ctx, "true", "-T")
	if err != nil {
		t.Fatal(err)
	}
	shell, err := s.Shell(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string][]string{"Command": cmd.Args[1:], "Shell": shell.Args[1:]} {
		end := slices.Index(a, "--")
		want := []string{"-o", "HostKeyAlias=" + brand.Name + "-box", "-o", "UserKnownHostsFile=" + sshPathArg(pinPath("box"), runtime.GOOS),
			"-o", "StrictHostKeyChecking=yes", "-o", "UpdateHostKeys=no", "-o", "GlobalKnownHostsFile=" + os.DevNull,
			"-o", "VerifyHostKeyDNS=no", "-o", "NoHostAuthenticationForLocalhost=no", "-o", "CheckHostIP=no",
			"-o", "FingerprintHash=sha256", "-o", "LogLevel=ERROR"}
		if !hasRun(a[:end], want) {
			t.Errorf("%s: %q lacks %q before the destination", name, a, want)
		}
		for _, o := range pinOpts {
			n := 0
			for _, x := range a {
				if strings.HasPrefix(x, o) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%s: %s %d times in %q", name, o, n, a)
			}
		}
		known, _ := sshKnowsOptionContext(ctx, "KnownHostsCommand=none")
		if got := slices.Contains(a, "KnownHostsCommand=none"); got != known {
			t.Errorf("%s: KnownHostsCommand=none is there: %v, and ssh knows it: %v", name, got, known)
		}
	}
	git, err := GitSSHCommand(ctx, s.Host)
	if err != nil || !strings.Contains(git, " -o 'HostKeyAlias="+brand.Name+"-box' ") {
		t.Errorf("GIT_SSH_COMMAND = %q, %v", git, err)
	}
}

// With the real ssh -G: a config that would undo the pin, in every place a
// config can say it, does not.
func TestThePinWinsOverTheConfig(t *testing.T) {
	userSSH := realSSH(t)
	undo := "StrictHostKeyChecking no\nUserKnownHostsFile /dev/null\nGlobalKnownHostsFile /tmp/global\nVerifyHostKeyDNS yes\n" +
		"UpdateHostKeys yes\nHostKeyAlias other\nNoHostAuthenticationForLocalhost yes\nCheckHostIP yes\n" +
		"LogLevel QUIET\nFingerprintHash md5\n"
	if ok, _ := sshKnowsOptionContext(t.Context(), "KnownHostsCommand=none"); ok {
		undo += "KnownHostsCommand /bin/echo\n"
	}
	if ok, _ := sshKnowsOptionContext(t.Context(), "NoHostAuthenticationForProxyCommand=no"); ok {
		undo += "NoHostAuthenticationForProxyCommand yes\n"
	}
	inc := writeFile(t, filepath.Join(userSSH, "included"), "Host *\n"+undo)
	writeFile(t, filepath.Join(userSSH, "config"), "Match all\n"+undo+"Include "+inc+"\n")
	cfg := writeFile(t, filepath.Join(t.TempDir(), "config"), undo)
	s := &SSH{Host: Host{Name: "box", SSH: "box.invalid", SSHConfig: cfg}}
	if err := SaveHosts(context.Background(), []Host{s.Host}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	cmd, err := s.Command(context.Background(), "true", "-T")
	if err != nil {
		t.Fatal(err)
	}
	vals := sshValues(t, cmd.Args[1:])
	for k, want := range map[string]string{
		"stricthostkeychecking": "true", "userknownhostsfile": pinPath("box"), "globalknownhostsfile": os.DevNull,
		"verifyhostkeydns": "false", "updatehostkeys": "false", "hostkeyalias": brand.Name + "-box",
		"nohostauthenticationforlocalhost": "no", "checkhostip": "no", "loglevel": "ERROR", "fingerprinthash": "SHA256",
	} {
		if got := first(vals, k); !strings.EqualFold(got, want) {
			t.Errorf("%s is %q, want %q", k, got, want)
		}
	}
	if v, ok := vals["knownhostscommand"]; ok {
		t.Errorf("knownhostscommand is %q, want none", v)
	}
	if v, ok := vals["nohostauthenticationforproxycommand"]; ok && first(vals, "nohostauthenticationforproxycommand") != "no" {
		t.Errorf("nohostauthenticationforproxycommand is %q", v)
	}
}

// What ssh prints when the pin stops a connection becomes the error of
// spec 14.14, a refusal. A ProxyJump hop's own known hosts do not, and
// neither do words in the middle of a line.
func TestHostKeyError(t *testing.T) {
	sshDirs(t)
	ed, ec := newKey(t, "ssh-ed25519"), newKey(t, "ecdsa-sha2-nistp256")
	h := Host{Name: "box", SSH: "box.example"}
	shown := "SHA256:77b0" + strings.Repeat("x", 39)
	changed := "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n" +
		"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n" +
		"@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n" +
		"IT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!\r\n" +
		"The fingerprint for the ED25519 key sent by the remote host is\r\n" + shown + ".\r\n" +
		"Please contact your system administrator.\r\n" +
		"Host key for " + brand.Name + "-box has changed and you have requested strict checking.\r\nHost key verification failed.\r\n"
	if hostKeyError(h, changed, 255) != nil {
		t.Error("a host without a pin got a pin's error")
	}
	if err := writeTestPin("box", []hostKey{ec, ed}); err != nil {
		t.Fatal(err)
	}
	ue := hostKeyError(h, changed, 255)
	if ue == nil || ue.Kind != KindRefused || ue.Code != "host_key_changed" ||
		ue.Msg != "box's host key changed: "+ed.Fingerprint()+" was pinned, and the host shows "+shown ||
		ue.Hint != "If you reinstalled box: "+brand.Name+" host rekey box. Otherwise, do not connect." {
		t.Fatalf("changed = %#v", ue)
	}
	ue = hostKeyError(h, "No RSA host key is known for "+brand.Name+"-box and you have requested strict checking.\nHost key verification failed.\n", 255)
	if ue == nil || !strings.HasSuffix(ue.Msg, "and the host shows a key of another type (RSA)") {
		t.Errorf("another type = %#v", ue)
	}
	for name, text := range map[string]string{
		"the hop's key":                          strings.ReplaceAll(changed, brand.Name+"-box", "jump.example"),
		"another host's pin":                     strings.ReplaceAll(changed, brand.Name+"-box", brand.Name+"-box2"),
		"words in the middle of a banner's line": "banner: Host key for " + brand.Name + "-box has changed and you have requested strict checking.\n",
		"ended early without verification line":  strings.TrimSuffix(changed, "Host key verification failed.\r\n"),
		"auth failure after a forged warning":    changed + "me@box.example: Permission denied (publickey).\r\n",
	} {
		if ue := hostKeyError(h, text, 255); ue != nil {
			t.Errorf("%s: %#v", name, ue)
		}
	}
	if ue := hostKeyError(h, changed, 1); ue != nil {
		t.Errorf("exit code 1: %#v", ue)
	}
	// ssh's exit with 255 goes through sshError from ssh's -E log, never stderr.
	if err := sshError(context.Background(), h, &exec.ExitError{ProcessState: exitState(t)}, changed, ""); !IsHostKeyError(err) {
		t.Errorf("sshError (-E log) = %v", err)
	}
	if err := sshError(context.Background(), h, &exec.ExitError{ProcessState: exitState(t)}, "Permission denied (publickey).\n", changed); IsHostKeyError(err) {
		t.Errorf("sshError (hop stderr) = %v, want no host key error", err)
	}
	// A pin that cannot be read refuses too.
	if err := os.WriteFile(pinPath("box"), []byte("# empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ue := hostKeyError(h, changed, 255); ue == nil || ue.Code != "host_key_unreadable" || ue.Kind != KindRefused {
		t.Errorf("an empty pin = %#v", ue)
	}
}
