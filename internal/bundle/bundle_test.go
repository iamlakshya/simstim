//go:build !windows

// These tests run the real host side, which runs on Linux and macOS only.

package bundle_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/bundle"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The tests in this binary may start it again as a session's holder.
func TestMain(m *testing.M) { hosttest.Main(m) }

// The private values of the canary test: each must be gone from every
// file of the bundle.
const (
	marker     = "CANARY-5b1e0d7a"
	fakeHost   = "vps-zorblax"
	fakeUser   = "qwyndolyn"
	fakeLaptop = "laptop-quixotic"
	fakeTopic  = "topic-rutabaga-4471"
	fakeToken  = "tk_" + "parsnip_90210" // in two parts, so a secret scanner does not take it for a key
	fakeEmail  = "qwyndolyn@example.org"
	fakeAddr   = "198.51.100.23"
	fakeAddr6  = "2001:db8::4277"
	fakeEnv    = "envtok-celeriac-31"
	fakeProxy  = "proxytok-kohlrabi-62"
	rawReady   = "\x1b[22;0t"
	detach     = "\x1c"
)

// victim is a session with no holder, as one that a kill ended, with the
// systemd unit that its manifest names. Its unit's state and the kernel's
// report come from a fake systemctl and journalctl.
const victim = "oomvictim2"

// The canary (decision 0025): with every category on, on both sides, a
// marker in a secret, the environment, the keys and the output reaches no
// file of the bundle. A fake host name, user, laptop and home folder,
// addresses, an email address and the alerts file's values are replaced,
// in every kind of file: records, logs, profiles, a saved window, the ssh
// log, the unit's state and the kernel's report.
func TestTheBundleHoldsNoCanary(t *testing.T) {
	h := hosttest.New(t)
	t.Setenv("CANARY_ENV", marker)
	t.Setenv("USER", fakeUser)
	bundle.SetHostname(t, fakeLaptop+".local")
	ch, _ := h.ClientHost(fakeHost)
	if err := client.SaveHosts([]client.Host{ch, {Name: "jumpy", SSH: fakeUser + "@jump.example.net:2222"}}); err != nil {
		t.Fatal(err)
	}
	if err := alerts.Save(paths.AlertsConfig(), api.AlertsConfig{Destinations: []api.AlertDestination{
		{Type: "ntfy", URL: "https://ntfy.example.com/" + fakeTopic, Token: fakeToken}, alertCommand}}); err != nil {
		t.Fatal(err)
	}
	s := canarySession(t, h, ch)
	privateLines(t, h, s.ID)
	unitFakes(t, h)

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := bundle.Write(ctx, bundle.Options{Host: &ch, Dir: dir, SSH: true})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(res.Path); err != nil || st.Mode().Perm() != 0o600 || filepath.Dir(res.Path) != dir ||
		!strings.HasPrefix(filepath.Base(res.Path), brand.Name+"-debug-") || strings.Contains(res.Path, fakeHost) {
		t.Fatalf("the bundle is %s: %v", res.Path, err)
	}
	if parts, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(parts) != 0 {
		t.Errorf("a part file stays: %v", parts)
	}
	files := unpack(t, res.Path)
	t.Logf("files %v\nleft %+v\nnotes %v\ncounts %+v", names(files), res.Left, res.Notes, res.Counts)
	old, v := "host/sessions/"+s.ID+"/holder.log.1", "host/sessions/"+victim+"/"
	window := "host/sessions/" + s.ID + "/debug/flight-20261016-000003.trace"
	for _, want := range []string{"README.txt", "replaced.txt", "laptop/doctor.txt", "laptop/outside/1.jsonl",
		"host/doctor.json", "host/ssh.log", "host/sessions/" + s.ID + "/debug.jsonl", "host/sessions/" + s.ID + "/holder.log", old,
		window, v + "debug.jsonl", v + "unit.txt", v + "kernel.txt"} {
		if _, ok := files[want]; !ok {
			t.Errorf("the bundle has no %s; it has %v", want, names(files))
		}
	}
	if !bytes.Equal(files[window], windowBytes) {
		t.Errorf("the saved window changed: %q", files[window])
	}
	if !bytes.Contains(files[v+"unit.txt"], []byte("Result=oom-kill")) || !bytes.Contains(files[v+"unit.txt"], []byte(unitCgroup)) ||
		!bytes.Contains(files[v+"kernel.txt"], []byte("Killed process 7 (node)")) {
		t.Errorf("unit.txt:\n%s\nkernel.txt:\n%s", files[v+"unit.txt"], files[v+"kernel.txt"])
	}
	profiles := 0
	for name, data := range files {
		if strings.HasSuffix(name, ".pprof") {
			profiles++
		}
		for _, bad := range []string{marker, fakeHost, fakeUser, fakeLaptop, fakeTopic, fakeToken, fakeEmail, fakeAddr, fakeAddr6,
			fakeEnv, fakeProxy, fakeRunToken, h.Home, "canary-fixer", "zorblax-app", "acme-crash", "PRIVATE-NOTES"} {
			if bytes.Contains(data, []byte(bad)) {
				t.Errorf("%s holds %q", name, bad)
			}
		}
	}
	if profiles < 2 {
		t.Errorf("the bundle has %d profiles, want the fresh heap and goroutine ones at least: %v", profiles, names(files))
	}
	got := map[string]int{}
	for _, c := range res.Counts {
		got[c.Kind] = c.N
	}
	for _, k := range []string{bundle.KindHost, bundle.KindUser, bundle.KindLaptop, bundle.KindHome, bundle.KindAddr,
		bundle.KindEmail, bundle.KindSession, bundle.KindAlert} {
		if got[k] == 0 {
			t.Errorf("nothing counted for %s: %+v", k, res.Counts)
		}
	}
	if !bytes.Contains(files[old], []byte("token="+alerts.Hidden)) || !bytes.Contains(files["laptop/outside/1.jsonl"], []byte("<host-")) {
		t.Errorf("the placeholders are missing:\n%s\n%s", files[old], files["laptop/outside/1.jsonl"])
	}
	// The user's own file, which outside.txt names by mistake, stays out.
	if _, ok := files["laptop/outside/2.jsonl"]; ok || !slices.ContainsFunc(res.Left, func(l bundle.Left) bool {
		return l.Name == "laptop/outside/2.jsonl" && strings.HasSuffix(l.From, "notes.txt")
	}) {
		t.Errorf("the user's own file: left out %+v", res.Left)
	}
}

// fakeRunToken is a token in an alert's command, which holds quotes: a
// holder's line escapes it once in holder.log, and twice in debug.jsonl.
// It is built in two parts, so a secret scanner does not take it for a key.
const fakeRunToken = "sk_" + "live_PLUM42"

var alertCommand = api.AlertDestination{Type: "command", Run: `curl -s -H "Authorization: Bearer ` + fakeRunToken + `" https://hooks.example.com/x`}

// alertLines are what a holder writes when an alert cannot go: a line of
// slog's text handler for holder.log, and a record with that text for
// debug.jsonl.
func alertLines(t *testing.T) (text, record []byte) {
	t.Helper()
	var b bytes.Buffer
	slog.New(slog.NewTextHandler(&b, nil)).Warn("alert not delivered", "type", "command", "target", alerts.Target(alertCommand), "error", "exit status 7")
	rec, err := json.Marshal(diag.Record{T: 1, Ev: diag.EvLog, Level: "WARN", Text: strings.TrimSpace(b.String())})
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), append(rec, '\n')
}

// windowBytes stand for a saved window of a program built with -trimpath,
// which holds no home folder, so that one ships in every run of the test.
var windowBytes = []byte("go 1.26 trace\x00\x01\x02 runtime/proc.go")

// unitCgroup is the cgroup of the victim's unit, as systemd shows it. The
// name of the user's manager in it is no email address.
var unitCgroup = "/user.slice/user-1000.slice/user" + "@1000.service/app.slice/" + host.UnitName(victim)

// privateLines writes lines that hold private values, as a log line might:
// in the client's file outside the debug folder, in a file of the host, in
// a session with no holder, and in a saved window.
func privateLines(t *testing.T, h *hosttest.Host, id string) {
	t.Helper()
	outside := filepath.Join(t.TempDir(), "acme-crash.jsonl")
	if _, err := client.StartDebug(client.DebugOptions{Cats: diag.AllCats, File: outside}); err != nil {
		t.Fatal(err)
	}
	client.DebugLog().Debug("private values", "host", fakeHost, "target", fakeHost+"/canary-fixer", "user", fakeUser,
		"laptop", fakeLaptop, "dir", "/home/"+fakeUser+"/src/app", "home", h.Home+"/notes", "addr", fakeAddr, "addr6", fakeAddr6,
		"mail", fakeEmail)
	client.StopDebug(io.Discard)
	// A file of the user's own, that outside.txt names by mistake.
	notes := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notes, []byte("PRIVATE-NOTES of "+fakeUser+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := os.OpenFile(filepath.Join(client.DebugDir(), diag.OutsideList), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = list.WriteString(notes + "\n")
	_ = list.Close()
	// In tests the laptop and the host share one state folder, so these
	// lines go where only the host looks: an old holder.log, and the
	// records of a session with no holder.
	text, record := alertLines(t)
	hostLine := "time=x level=INFO msg=alert url=https://ntfy.example.com/" + fakeTopic + " token=" + fakeToken + " from " + fakeAddr + "\n"
	if err := diag.WriteFile(paths.HolderLog(id)+".1", append([]byte(hostLine), text...)); err != nil {
		t.Fatal(err)
	}
	if err := diag.WriteFile(filepath.Join(paths.SessionDebugDir(id), "flight-20261016-000003.trace"), windowBytes); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(paths.SessionDir(victim)) })
	m := &host.Manifest{ID: victim, Name: "oom-victim", CreatedAt: time.Now().Add(-time.Hour), Cwd: h.Home + "/src/zorblax-app",
		Run: host.Runtime{Unit: host.UnitName(victim)}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	line := `{"t":1,"ev":"log","text":"oom-victim on ` + fakeHost + ` from ` + fakeAddr6 + `"}` + "\n"
	if err := diag.WriteFile(paths.DebugTrace(victim), append([]byte(line), record...)); err != nil {
		t.Fatal(err)
	}
}

// unitFakes puts a fake systemctl and journalctl on $PATH, lets the host
// read the journal, and gives every host one ssh -v log. Each holds the
// private values.
func unitFakes(t *testing.T, h *hosttest.Host) {
	t.Helper()
	unit := host.UnitName(victim)
	h.Script("systemctl", `case " $* " in *" show "*) printf 'ActiveState=failed\nResult=oom-kill\nControlGroup=`+unitCgroup+`\n' ;; *) exit 1 ;; esac`)
	h.Script("journalctl", `printf '2026-10-16T18:57:12+0000 `+fakeHost+` kernel: oom-kill:constraint=CONSTRAINT_MEMCG,oom_memcg=`+unitCgroup+`,task=node,pid=7,uid=1000\n'
printf '2026-10-16T18:57:12+0000 `+fakeHost+` kernel: Memory cgroup out of memory: Killed process 7 (node) total-vm:1kB\n'
printf '2026-10-16T18:57:13+0000 `+fakeHost+` kernel: `+unit+` peer `+fakeAddr6+`\n'
printf '2026-10-16T18:57:14+0000 `+fakeHost+` kernel: another line\n'`)
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
	old := host.JournalAccess
	host.JournalAccess = func() bool { return true }
	t.Cleanup(func() { host.JournalAccess = old })
	bundle.SetVerboseLog(t, []byte("debug1: Connecting to "+fakeHost+" ["+fakeAddr6+"] port 22.\n"+
		"debug1: Executing proxy command: exec tunnel --secret "+fakeProxy+"\n"+
		"debug1: Sending env LC_TOKEN = "+fakeEnv+"\n"+
		"debug1: Authenticating to "+fakeHost+":22 as '"+fakeUser+"'\n"))
}

// canarySession starts a session whose secret, environment, arguments,
// keys and output all hold the marker, with every host category on, and
// attaches to it with every category on the client's side.
func canarySession(t *testing.T, h *hosttest.Host, ch client.Host) *host.SessionInfo {
	t.Helper()
	keys := filepath.Join(h.Home, "keys.bin")
	s, err := host.New(host.NewArgs{Name: "canary-fixer", Cwd: "~", Cols: 80, Rows: 24,
		Secrets: map[string]api.Secret{"CANARY_SECRET": marker},
		Argv: []string{"/bin/sh", "-c", `stty raw -echo; printf 'READY %s %s\n' "$CANARY_SECRET" "$CANARY_ENV"; head -c 15 > ` + keys +
			`; printf 'DONE %s\n' "$1"; exec sleep 60`, "canary", marker}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartDebug(client.DebugOptions{Cats: diag.AllCats}); err != nil {
		t.Fatal(err)
	}
	defer client.StopDebug(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.DebugStart(ctx, s.ID, diag.HostCats, time.Minute); err != nil {
		t.Fatal(err)
	}
	term := hosttest.NewTerminal(t, 80, 24)
	var state client.AttachState
	done := make(chan error, 1)
	go func() {
		_, err := client.Attach(ctx, client.AttachOptions{Host: ch, Session: s.ID, Settings: client.DefaultSettings(), State: &state, In: term.TTY, Out: term.TTY})
		done <- err
	}()
	term.WaitFor("READY " + marker)
	term.WaitFor(rawReady)
	term.Type(marker)
	term.WaitFor("DONE " + marker)
	if _, err := conn.DebugSave(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	term.Type(detach)
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	return s
}

// unpack reads every file of a bundle, gunzipped when it is a profile, and
// checks that each entry is private.
func unpack(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(z)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Uname != "" || hdr.Gname != "" || hdr.Uid != 0 {
			t.Errorf("%s names its owner: %+v", hdr.Name, hdr)
		}
		if hdr.Typeflag == tar.TypeDir {
			if hdr.Mode != 0o700 {
				t.Errorf("the folder %s has mode %o", hdr.Name, hdr.Mode)
			}
			continue
		}
		if hdr.Mode != 0o600 {
			t.Errorf("%s has mode %o", hdr.Name, hdr.Mode)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(hdr.Name, ".pprof") {
			pz, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s: %v", hdr.Name, err)
			}
			if data, err = io.ReadAll(pz); err != nil {
				t.Fatalf("%s: %v", hdr.Name, err)
			}
		}
		out[hdr.Name] = data
	}
}

func names(files map[string][]byte) []string {
	var out []string
	for n := range files {
		out = append(out, n)
	}
	return out
}
