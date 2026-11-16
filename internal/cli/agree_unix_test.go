//go:build unix

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/tui"
)

func TestShowScreen(t *testing.T) {
	newTestHost(t)
	// Include \u200c (\342\200\214), \u200d (\342\200\215) and \U000e0041 (\363\240\201\201),
	// which survive the VT emulator as grapheme clusters and require api.CleanText.
	id := newSession(t, "scr", "/bin/sh", "-c",
		`printf '\033[2J\033[H\033[32mhello screen\033[0m   \r\nsec\342\200\214ond\342\200\215\363\240\201\201\033]52;c;aGk=\007\342\200\256 line   \r\n\033[4;3Hplaced'; sleep 60`)
	hosttest.WaitFor(t, 5*time.Second, "the session's screen bytes", func() bool {
		return strings.Contains(peekOutput(t, id), "placed")
	})

	out, errOut, err := runApart(t, "show", "box/scr", "--screen")
	if err != nil || errOut != "" {
		t.Fatalf("show --screen: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
	for _, bad := range []string{"\x1b", "\x07", "\u202e", "\u200c", "\u200d", "\U000e0041"} {
		if strings.Contains(out, bad) {
			t.Fatalf("show --screen let %q through in %q", bad, out)
		}
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 24 || lines[0] != "hello screen" || lines[1] != "second line" || lines[2] != "" || lines[3] != "  placed" {
		t.Fatalf("show --screen lines = %q", lines[:min(len(lines), 6)])
	}

	rawJSON := okJSON(t, "show", "box/scr", "--screen", "--json")
	checkJSON(t, "show --screen", rawJSON, false)
	for _, bad := range []string{"\x1b", "\x07", "\u202e", "\u200c", "\u200d", "\U000e0041"} {
		if strings.Contains(rawJSON, bad) {
			t.Fatalf("show --screen --json let %q through in %q", bad, rawJSON)
		}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["id"] != id || obj["address"] != "box/"+id {
		t.Errorf("show --screen --json session = %v", obj)
	}
	scrJSON, ok := obj["screen"].([]any)
	if !ok || len(scrJSON) != 24 || scrJSON[0] != "hello screen" || scrJSON[1] != "second line" || scrJSON[3] != "  placed" {
		t.Fatalf("show --screen --json screen = %#v", obj["screen"])
	}

	// Once the session's holder stops, show --screen fails cleanly and points to resume.
	if _, err := host.HolderRequest(id, "kill", nil, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, "the holder to stop", func() bool {
		_, err := host.HolderRequest(id, "peek", nil, 200*time.Millisecond)
		return err != nil
	})

	_, err = run(t, "show", "box/"+id, "--screen")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "session_gone" || ue.Hint != brand.Name+" resume box/"+id {
		t.Fatalf("show --screen after holder stop = %#v (%v)", ue, err)
	}
}
func TestHostTestReconnect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OpenSSH ControlMaster is Unix-only")
	}
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")

	eventsFile := filepath.Join(th.h.Home, "master.events")
	modeFile := filepath.Join(th.h.Home, "master.mode")
	stateDir := filepath.Join(th.h.Home, "cmstate")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Stateful fake ssh modelling OpenSSH ControlMaster lifecycle:
	// - bridge dial reuses an active master if present, else creates a new master ID
	// - -O check reports running iff a master file exists
	// - -O exit removes the master file in normal mode, or simulates stuck/denied/hung masters
	th.h.Script("ssh", `mode=$(cat "`+modeFile+`" 2>/dev/null || echo normal)
for a; do prev=$last; last=$a; done
case " $* " in
  *" -O exit "*)
    host="$last"
    mf="`+stateDir+`/$host"
    case "$mode" in
      denied)
        echo "retire-denied:$host" >> "`+eventsFile+`"
        echo "Control socket connect(/tmp/cm): Operation not permitted" >&2
        exit 255
        ;;
      hang)
        echo "retire-hang:$host" >> "`+eventsFile+`"
        sleep 5
        exit 0
        ;;
      ignore)
        echo "retire-ignored:$host" >> "`+eventsFile+`"
        echo "Exit request sent." >&2
        exit 0
        ;;
      *)
        if [ -f "$mf" ]; then
          id=$(cat "$mf")
          rm -f "$mf"
          echo "retire:$host:$id" >> "`+eventsFile+`"
          echo "Exit request sent." >&2
          exit 0
        fi
        echo "retire-absent:$host" >> "`+eventsFile+`"
        echo "Control socket connect(/tmp/cm): No such file or directory" >&2
        exit 255
        ;;
    esac
    ;;
  *" -O check "*)
    host="$last"
    mf="`+stateDir+`/$host"
    if [ -f "$mf" ]; then
      echo "Master running (pid=4242)" >&2
      exit 0
    fi
    echo "Control socket connect(/tmp/cm): No such file or directory" >&2
    exit 255
    ;;
esac
host="$prev"
if [ "$host" = "dead" ]; then
  echo "ssh: connect to host dead port 22: Connection refused" >&2
  exit 255
fi
mf="`+stateDir+`/$host"
seqf="`+stateDir+`/seq"
if [ -f "$mf" ]; then
  id=$(cat "$mf")
  echo "dial:reuse:$host:$id" >> "`+eventsFile+`"
else
  seq=$(cat "$seqf" 2>/dev/null || echo 0)
  id=$((seq + 1))
  echo "$id" > "$seqf"
  echo "$id" > "$mf"
  echo "dial:new:$host:$id" >> "`+eventsFile+`"
fi
exec sh -c "$last"
`)

	readEvents := func() string {
		b, _ := os.ReadFile(eventsFile)
		return strings.TrimSpace(string(b))
	}
	clearEvents := func() { _ = os.Remove(eventsFile) }

	if err := client.SaveHosts(t.Context(), []client.Host{{Name: "plain", SSH: "box"}}); err != nil {
		t.Fatal(err)
	}

	// (a) Absent master: --reconnect succeeds cleanly and creates master 1.
	clearEvents()
	if out := ok(t, "host", "test", "plain", "--reconnect"); !strings.HasPrefix(out, "plain answers: connect ") {
		t.Fatalf("absent master host test --reconnect = %q", out)
	}
	if got := readEvents(); got != "retire-absent:box\ndial:new:box:1" {
		t.Fatalf("absent master events =\n%s", got)
	}

	// Plain `host test` without --reconnect reuses master 1 without retiring it.
	clearEvents()
	if out := ok(t, "host", "test", "plain"); !strings.HasPrefix(out, "plain answers: connect ") {
		t.Fatalf("plain host test = %q", out)
	}
	if got := readEvents(); got != "dial:reuse:box:1" {
		t.Fatalf("plain host test events =\n%s", got)
	}

	// (b) Active master: `host test --reconnect` retires master 1 and dials fresh master 2.
	clearEvents()
	rawJSON := okJSON(t, "host", "test", "plain", "--reconnect", "--json")
	checkJSON(t, "host test --reconnect", rawJSON, false)
	if got := readEvents(); got != "retire:box:1\ndial:new:box:2" {
		t.Fatalf("active master CLI reconnect events =\n%s", got)
	}

	// Exercise the launcher's "host: reconnect" against the same stateful master:
	// startup reuses master 2, and palette "host: reconnect" retires master 2 and dials master 3.
	drv := newTUIDriver(t)
	drv.waitOnline("plain")
	if got := readEvents(); !strings.Contains(got, "dial:reuse:box:2") {
		t.Fatalf("launcher startup events =\n%s", got)
	}

	clearEvents()
	drv.reconnectFromPalette()
	drv.waitOnline("plain")
	if got := readEvents(); got != "retire:box:2\ndial:new:box:3" {
		t.Fatalf("launcher palette reconnect events =\n%s", got)
	}

	// (c) Stuck master (denied and timed-out): both `host test --reconnect` and
	// launcher "host: reconnect" fail without dialling over stale master 3.
	restoreTimeout := client.SetMasterExitTimeout(80 * time.Millisecond)
	defer restoreTimeout()

	for _, mode := range []string{"denied", "ignore", "hang"} {
		if err := os.WriteFile(modeFile, []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
		clearEvents()
		failsWithHint(t, "could not close the shared SSH connection to plain", "doctor --fix", "host", "test", "plain", "--reconnect")
		if got := readEvents(); strings.Contains(got, "dial:") {
			t.Fatalf("CLI reconnect in mode %s dialled over stuck master:\n%s", mode, got)
		}

		clearEvents()
		drv.reconnectFromPalette()
		err := drv.waitFailed("plain")
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "reconnect_failed" {
			t.Fatalf("launcher reconnect in mode %s = %#v (%v)", mode, ue, err)
		}
		if got := readEvents(); strings.Contains(got, "dial:") {
			t.Fatalf("launcher reconnect in mode %s dialled over stuck master:\n%s", mode, got)
		}
	}

	// Restore normal mode and timeout, then verify non-SSH and unreachable SSH hosts.
	restoreTimeout()
	_ = os.WriteFile(modeFile, []byte("normal"), 0o600)
	drv.close()

	noSSH, _ := th.h.ClientHost("nossh")
	if err := saveHost(noSSH); err != nil {
		t.Fatal(err)
	}
	clearEvents()
	if out := ok(t, "host", "test", "nossh", "--reconnect"); !strings.HasPrefix(out, "nossh answers: connect ") {
		t.Fatalf("host test nossh --reconnect = %q", out)
	}
	if got := readEvents(); got != "" {
		t.Errorf("non-SSH host ran ssh:\n%s", got)
	}

	if err := saveHost(client.Host{Name: "dead", SSH: "dead"}); err != nil {
		t.Fatal(err)
	}
	clearEvents()
	failsWithHint(t, "cannot reach dead", "ssh", "host", "test", "dead", "--reconnect")
	if got := readEvents(); !strings.HasPrefix(got, "retire-absent:dead") {
		t.Errorf("unreachable host events =\n%s", got)
	}
}
func (d *tuiDriver) waitOnline(host string) {
	d.t.Helper()
	hosttest.WaitFor(d.t, 5*time.Second, host+" online in launcher", func() bool {
		var ok bool
		d.do(func(m *tui.Model) { ok = m.HostOnline(host) })
		return ok
	})
}
func (d *tuiDriver) waitFailed(host string) error {
	d.t.Helper()
	var got error
	hosttest.WaitFor(d.t, 5*time.Second, host+" failed in launcher", func() bool {
		var failed bool
		d.do(func(m *tui.Model) { failed, got = m.HostFailed(host) })
		return failed
	})
	return got
}
