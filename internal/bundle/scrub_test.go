package bundle_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/bundle"
)

// The home folders of the tests, built at run time, so that no tracked
// file spells a real user's home folder (scripts/check-private.sh).
var (
	usersAlice = "/Users" + "/alice"
	homeAlice  = "/home" + "/alice"
)

func testScrubber() *bundle.Scrubber {
	return bundle.NewScrubber(bundle.Values{
		Hosts:    []string{"my-vps", "alice@build.example.net:2222", "dev", "203.0.113.50"},
		Laptop:   []string{"alices-mbp.local", "alices-mbp"},
		Users:    []string{"alice", "ubuntu"},
		Homes:    []string{usersAlice, homeAlice},
		Sessions: []string{"api", "fix login bug", "net"},
		Folders:  []string{"~/src/acme", homeAlice + "/src/acme/web", "/tmp", "~"},
		Keys:     []string{"~/.ssh/id_work"},
	})
}

// jsonString is s as JSON writes it in a string, with its escapes.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// The rules match whole tokens and whole paths only, and leave generic
// names, the records' own words and loopback alone.
func TestTheRulesMatchWholeTokensAndPaths(t *testing.T) {
	fp := "SHA256:" + strings.Repeat("Ab1+", 10) + "xyz"
	cases := []struct{ in, want string }{
		{"session=api host=my-vps", "session=<session-1> host=<host-1>"},
		{"github.com/x/internal/api.Secret", "github.com/x/internal/api.Secret"},
		{"attach my-vps/api.", "attach <host-1>/<session-1>."},
		{"ssh://my-vps:22/x", "ssh://<host-1>:22/x"},
		{`{"user":"alice","cat":"net"}`, `{"user":"<user>","cat":"net"}`},
		{"user=ubuntu on dev as root", "user=ubuntu on dev as root"},
		{usersAlice + "/src/acme/main.go:12", "~/<folder-1>/main.go:12"},
		{"cwd=" + homeAlice + "/src/acme/web.", "cwd=~/<folder-2>."},
		{homeAlice + "2/x " + "/home" + "/bob/y", "~/x ~/y"},
		{"/home/ubuntu/x /home/linuxbrew/.linuxbrew/bin /tmp/x", "/home/ubuntu/x /home/linuxbrew/.linuxbrew/bin /tmp/x"},
		{"connect to 203.0.113.7 port 22, again 203.0.113.7.", "connect to <addr-1> port 22, again <addr-1>."},
		{"[2001:db8::7]:22", "[<addr-2>]:22"},
		{"127.0.0.1 ::1 0.0.0.0 at 12:34:56 v1.2.3", "127.0.0.1 ::1 0.0.0.0 at 12:34:56 v1.2.3"},
		{"mail alice@example.com now", "mail <email> now"},
		{"key " + fp + " ok", "key <fingerprint> ok"},
		{"name: fix login bug.", "name: <session-2>."},
		{"box.example-tailnet.ts.net up", "<host-3> up"},
		{"Offering public key: " + usersAlice + "/.ssh/id_work ED25519", "Offering public key: <key-1> ED25519"},
		{"from alices-mbp.local and ALICES-MBP", "from <laptop> and <laptop>"},
		{"build.example.net", "<host-2>"},
		{`text":"err=\"dial my-vps: refused\"\nmy-vps"`, `text":"err=\"dial <host-1>: refused\"\n<host-1>"`},
		{"<host-1> stays", "<host-1> stays"},
		// The gaps that the review found.
		{jsonString("dial <my-vps>: refused"), jsonString("dial <") + "<host-1>" + jsonString(">: refused")},
		{jsonString("a=1&host=my-vps&u=alice"), "a=1" + jsonString("&") + "host=<host-1>" + jsonString("&") + "u=<user>"},
		{"connect my-vps.internal ok", "connect <host-1>.internal ok"},
		{"host|my-vps|up a+my-vps #my-vps", "host|<host-1>|up a+<host-1> #<host-1>"},
		{"host my-vps", "host <host-1>"},
		{"user alice/my-vps", "user <user>/<host-1>"},
		{"file=%2FUsers%2Falice%2Fx u=alice%40box", "file=~%2Fx u=<user>%40box"},
		{"/users/alice/x " + "/home" + "/josé/y", "~/x ~/y"},
		{"from 198.051.100.023", "from <addr-3>"},
		{"ControlGroup=/user.slice/user-1000.slice/user" + "@1000.service/app.slice", "ControlGroup=/user.slice/user-1000.slice/user" + "@1000.service/app.slice"},
	}
	s := testScrubber()
	for _, c := range cases {
		got := string(s.Scrub([]byte(c.in)))
		if got != c.want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
		if k := s.Check([]byte(got)); k != "" {
			t.Errorf("Check(%q) still finds %s", got, k)
		}
		if again := string(s.Scrub([]byte(got))); again != got {
			t.Errorf("a second Scrub changed %q to %q", got, again)
		}
	}
}

// The last check searches for each known value on its own, in any case,
// so a value that the rules miss keeps its file out. A value inside a
// longer word, or after a slash, does not.
func TestTheLastCheckFindsWhatTheRulesMiss(t *testing.T) {
	s := testScrubber()
	for in, want := range map[string]string{
		"ssh_my-vps":                      bundle.KindHost,
		"MY-VPS_2":                        bundle.KindHost,
		"émy-vps":                         bundle.KindHost,
		"x" + homeAlice + "y/z":           "",
		"x " + strings.ToUpper(homeAlice): bundle.KindHome,
		"internal/api and fix-api":        "",
		"apis and api2":                   "",
		"from 203.0.113.9":                bundle.KindAddr,
		"<host-1> <user> (hidden)":        "",
	} {
		if got := s.Check([]byte(in)); got != want {
			t.Errorf("Check(%q) = %q, want %q", in, got, want)
		}
	}
}

// A profile keeps its function names, and loses the home folder from the
// file name of each mapping and function. It stays a profile that the
// reader can read again.
func TestAProfileLosesItsHomeFolder(t *testing.T) {
	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 0); err != nil {
		t.Fatal(err)
	}
	_, self, _, _ := runtime.Caller(0)
	home := filepath.Dir(filepath.Dir(self)) // internal/, as if it were a home folder
	s := bundle.NewScrubber(bundle.Values{Homes: []string{home}})
	out, table, err := bundle.RewriteProfile(buf.Bytes(), s.RewritePath)
	if err != nil {
		t.Fatal(err)
	}
	files, funcs := 0, 0
	for _, str := range table {
		if strings.Contains(str.S, home) {
			t.Errorf("a string still holds the home folder: %s", str.S)
		}
		if str.File && strings.HasPrefix(str.S, "~/bundle/") {
			files++
		}
		if strings.Contains(str.S, "TestAProfileLosesItsHomeFolder") {
			funcs++
		}
	}
	if files == 0 || funcs == 0 {
		t.Fatalf("files from ~ %d, this function's name %d", files, funcs)
	}
	z, err := gzip.NewReader(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := io.ReadAll(z); err != nil || bytes.Contains(plain, []byte(home)) {
		t.Fatalf("the rewritten profile: %v", err)
	}
	if _, again, err := bundle.RewriteProfile(out, s.RewritePath); err != nil || len(again) != len(table) {
		t.Fatalf("the rewritten profile does not read again: %v, %d strings, want %d", err, len(again), len(table))
	}
	if _, _, err := bundle.RewriteProfile([]byte("not a profile"), s.RewritePath); err == nil {
		t.Error("a file that is not a profile was read")
	}
}

// A saved window holds a home folder when its program was built without
// -trimpath; a generic one does not count.
func TestASavedWindowWithAHomeFolder(t *testing.T) {
	s := testScrubber()
	if !s.HoldsHome([]byte("\x00\x05"+usersAlice+"/go/pkg\x00")) || !s.HoldsHome([]byte("x/home"+"/carol/src")) {
		t.Error("a home folder went unseen")
	}
	if s.HoldsHome([]byte("\x00/home/runner/work/x\x00runtime/proc.go")) {
		t.Error("a generic home folder counted")
	}
}
