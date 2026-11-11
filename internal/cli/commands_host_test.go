package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/provider/lima"
)

func TestHostCommands(t *testing.T) {
	th := newTestHost(t)
	if out := ok(t, "host", "ls"); out != "box              box\n" {
		t.Errorf("host ls = %q", out)
	}
	var hosts []client.Host
	if err := json.Unmarshal([]byte(ok(t, "host", "ls", "--json")), &hosts); err != nil || len(hosts) != 1 || hosts[0].Machine == nil {
		t.Errorf("host ls --json = %+v, %v", hosts, err)
	}
	if out := ok(t, "host", "test", "box"); !strings.HasPrefix(out, "box answers: connect ") {
		t.Errorf("host test = %q", out)
	}
	out := ok(t, "host", "doctor", "box")
	for _, want := range []string{"box: ", "systemd user manager", "runtime folder", "Agents:", "✕ Claude Code      not installed", "Try: " + brand.Name + " agents install claude --host box"} {
		if !strings.Contains(out, want) {
			t.Errorf("host doctor lacks %q:\n%s", want, out)
		}
	}
	var d client.HostInfo
	if err := json.Unmarshal([]byte(ok(t, "host", "doctor", "box", "--json")), &d); err != nil || d.Agents["shell"] != "/bin/sh" {
		t.Errorf("host doctor --json = %+v, %v", d, err)
	}
	if out := ok(t, "host", "restore", "box"); out != "Nothing to restore on box.\n" {
		t.Errorf("host restore = %q", out)
	}
	if out := ok(t, "host", "status", "box"); !strings.Contains(out, `"state":"running"`) {
		t.Errorf("host status = %q", out)
	}
	if out := ok(t, "host", "start", "box"); out != "box is running.\n" {
		t.Errorf("host start = %q", out)
	}
	if out := ok(t, "host", "stop", "box"); !strings.HasPrefix(out, "box is stopped.") {
		t.Errorf("host stop = %q", out)
	}
	fails(t, "is not an idle time", "host", "idle", "box", "soon")
	if out := ok(t, "host", "idle", "box", "45m"); out != "Saved, but hosttest hosts do not stop by themselves.\n" {
		t.Errorf("host idle = %q", out)
	}
	if h, _ := client.FindHost("box"); h.Machine.IdleStop != "45m" {
		t.Errorf("idle stop saved as %q", h.Machine.IdleStop)
	}
	_ = th
}

// The doctor adds the loopback check of a Lima VM, on this computer. The
// test machine stands in for the VM: a fake limactl lists it, and the test
// machine's provider gets the check that the doctor keeps for "lima".
func TestHostDoctorChecksALimaVM(t *testing.T) {
	th := newTestHost(t)
	if out := ok(t, "host", "doctor", "box"); strings.Contains(out, lima.LoopbackName) {
		t.Errorf("host doctor checked the loopback of a host that is not a Lima VM:\n%s", out)
	}
	limaCheck := machineChecks["lima"]
	if limaCheck == nil {
		t.Fatalf("host doctor has no check for the provider %q: %v", "lima", machineChecks)
	}
	machineChecks[hosttest.Provider] = limaCheck
	t.Cleanup(func() { delete(machineChecks, hosttest.Provider) })
	list := filepath.Join(th.h.Root, "limactl-list.json")
	th.h.Script("limactl", `[ "$1" = list ] && cat "`+list+`"`)
	inst := th.ch.Machine.ID
	// The VM's folder, with the ha.pid that its host agent wrote at its start.
	vmDir := filepath.Join(th.h.Root, "lima", "vm")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, "ha.pid"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(vmDir)
	write := func(config string) {
		t.Helper()
		line := `{"name":"` + inst + `","status":"Running","dir":` + string(quoted) + `,"vmType":"vz","config":` + config + "}\n"
		if err := os.WriteFile(list, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A VM made before host create closed the path has no hostLoopback.
	write(`{}`)
	out := ok(t, "host", "doctor", "box")
	for _, want := range []string{
		"  ✕ laptop loopback        box can reach services on your Mac's loopback address\n",
		"    Try: limactl stop " + inst + ", then limactl edit --set '.networkOpts.hostLoopback = false' " + inst + ", then limactl start " + inst + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("host doctor lacks %q:\n%s", want, out)
		}
	}
	write(`{"networkOpts":{"hostLoopback":false}}`)
	if out := ok(t, "host", "doctor", "box"); !strings.Contains(out, "  ✓ laptop loopback        box cannot reach services on your Mac's loopback address\n") {
		t.Errorf("host doctor on a closed VM:\n%s", out)
	}
	var d client.HostInfo
	if err := json.Unmarshal([]byte(ok(t, "host", "doctor", "box", "--json")), &d); err != nil || len(d.Checks) == 0 ||
		d.Checks[len(d.Checks)-1].Name != lima.LoopbackName || !d.Checks[len(d.Checks)-1].OK {
		t.Errorf("host doctor --json = %+v, %v", d.Checks, err)
	}
	// The VM's lima.yaml changed after its start: the running VM keeps the
	// setting it started with.
	later := time.Now().Add(time.Hour)
	if err := os.WriteFile(filepath.Join(vmDir, "lima.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(vmDir, "lima.yaml"), later, later); err != nil {
		t.Fatal(err)
	}
	out = ok(t, "host", "doctor", "box")
	for _, want := range []string{
		"  ✕ laptop loopback        the Lima settings of box changed after its VM started, so they apply only after a restart\n",
		"    Try: limactl stop " + inst + ", then limactl start " + inst + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("host doctor lacks %q:\n%s", want, out)
		}
	}
}

func TestHostCommandsWithoutAMachine(t *testing.T) {
	th := newTestHost(t)
	script := th.h.Script("provider", `echo "$1 $(cat)" > "$HOME/provider.log"; [ "$1" = status ] && echo '{"state":"running"}'; true`)
	if err := saveHost(client.Host{Name: "plain", SSH: "box", Provider: &client.Provider{Script: script}}); err != nil {
		t.Fatal(err)
	}
	if out := ok(t, "host", "status", "plain"); out != "{\"state\":\"running\"}\n" {
		t.Errorf("host status = %q", out)
	}
	if out := ok(t, "host", "stop", "plain"); out != "plain: stop done.\n" {
		t.Errorf("host stop = %q", out)
	}
	if b, _ := os.ReadFile(filepath.Join(th.h.Home, "provider.log")); !strings.Contains(string(b), `"name":"plain"`) {
		t.Errorf("the script got %q", b)
	}
	fails(t, "was not made by `host create`", "host", "idle", "plain", "30m")
	fails(t, "nothing to destroy", "host", "destroy", "plain", "--yes")
	if out := ok(t, "host", "ls"); !strings.Contains(out, "plain            box") {
		t.Errorf("host ls = %q", out)
	}
}

func TestHostRmAndDestroy(t *testing.T) {
	newTestHost(t)
	newSession(t, "busy", "sleep", "60")
	failsWithHint(t, "are working: busy", "--force", "host", "destroy", "box", "--yes")
	if out := ok(t, "host", "destroy", "box", "--yes", "--force"); !strings.HasPrefix(out, "Deleted the hosttest machine ") {
		t.Errorf("destroy = %q", out)
	}
	fails(t, `no host named "box"`, "host", "rm", "box")
	if err := saveHost(client.Host{Name: "kept", SSH: "box", Machine: &client.Machine{Provider: "lima", ID: "vm1"}}); err != nil {
		t.Fatal(err)
	}
	out := ok(t, "host", "rm", "kept")
	if !strings.HasPrefix(out, "Forgot kept.") || !strings.Contains(out, "Its lima machine vm1 is kept.") {
		t.Errorf("host rm = %q", out)
	}
	if out := ok(t, "host", "ls"); !strings.HasPrefix(out, "No hosts yet.") {
		t.Errorf("host ls = %q", out)
	}
	fails(t, "no hosts yet", "ls")
}

func TestAlertsCommands(t *testing.T) {
	th := newTestHost(t)
	if out := ok(t, "host", "alerts", "box"); !strings.HasPrefix(out, "Alerts on box are off: there is no ~/.config/") {
		t.Errorf("alerts = %q", out)
	}
	fails(t, "nothing to set", "host", "alerts", "set", "box")
	fails(t, "--format needs --webhook", "host", "alerts", "set", "box", "--format", "slack")
	fails(t, "--token-cmd needs --ntfy", "host", "alerts", "set", "box", "--token-cmd", "echo x")
	got := filepath.Join(th.h.Home, "alert.json")
	out := ok(t, "host", "alerts", "set", "box", "--command", "cat > "+got)
	if !strings.Contains(out, "Alerts on box are on") || !strings.Contains(out, "→ command  cat > "+got) {
		t.Errorf("alerts set = %q", out)
	}
	out = ok(t, "host", "alerts", "test", "box", "--event", "needs_input")
	if !strings.HasPrefix(out, "Sent a test alert from box:\n  ✓ command") {
		t.Errorf("alerts test = %q", out)
	}
	if b, _ := os.ReadFile(got); !strings.Contains(string(b), `"needs_input"`) {
		t.Errorf("the command got %q", b)
	}
	out = ok(t, "host", "alerts", "set", "box", "--add", "--ntfy", "https://ntfy.example.com/topic", "--token-cmd", "echo tok-123",
		"--events", "failed,oom", "--delay", "30s", "--min-turn", "2m", "--detail")
	for _, want := range []string{"events   failed, oom", "needs_input waits 30s", "turns of 2m0s or more", "detail on", "→ ntfy     https://ntfy.example.com/…  (token set)"} {
		if !strings.Contains(out, want) {
			t.Errorf("alerts set --add lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tok-123") {
		t.Error("the token was printed")
	}
	out = ok(t, "host", "alerts", "set", "box", "--add", "--webhook", "https://hooks.example.com/x", "--format", "slack")
	if !strings.Contains(out, "→ webhook  https://hooks.example.com/…  (slack)") {
		t.Errorf("alerts set webhook = %q", out)
	}
	var both map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ok(t, "host", "alerts", "box", "--json")), &both); err != nil || both["alerts"] == nil {
		t.Errorf("alerts --json = %v", err)
	}
	if out := ok(t, "host", "alerts", "log", "box"); !strings.Contains(out, "needs_input") && out != "box has sent no alerts yet.\n" {
		t.Errorf("alerts log = %q", out)
	}
	ok(t, "host", "alerts", "log", "box", "--json", "-n", "3")
	if out := ok(t, "host", "alerts", "off", "box"); out != "Alerts on box are off: its alerts file is gone.\n" {
		t.Errorf("alerts off = %q", out)
	}
	if out := ok(t, "host", "alerts", "off", "box"); out != "Alerts on box were already off.\n" {
		t.Errorf("alerts off again = %q", out)
	}
	fails(t, "the token command failed", "host", "alerts", "set", "box", "--ntfy", "https://ntfy.example.com/t", "--token-cmd", "exit 3")
}

// stdout runs fn and returns what it printed.
func stdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	err = fn()
	os.Stdout = old
	_ = w.Close()
	return <-out, err
}

func TestAddHost(t *testing.T) {
	th := newTestHost(t)
	added := th.ch
	added.Name = "added"
	ctx := context.Background()

	// With no helper on the host, add installs this build's, when it has
	// one for the host's platform.
	out, err := stdout(t, func() error { return addHost(ctx, added, true) })
	if _, embedded := helpers.For(runtime.GOOS, runtime.GOARCH); runtime.GOOS == "linux" && embedded {
		if err != nil || !strings.Contains(out, "✓ installed "+brand.HostBinary+" "+client.HelperVersion()) {
			t.Fatalf("add = %v\n%s", err, out)
		}
	} else {
		var ue *client.UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "no helper for "+runtime.GOOS+"/") {
			t.Fatalf("add on a host this build has no helper for = %v\n%s", err, out)
		}
		// With the helper in place, as a release installs it, add goes on.
		th.h.Install(client.HelperVersion(), "")
		if out, err = stdout(t, func() error { return addHost(ctx, added, false) }); err != nil {
			t.Fatalf("add = %v\n%s", err, out)
		}
		if !strings.Contains(out, "is already installed") {
			t.Errorf("add = %q", out)
		}
	}
	for _, want := range []string{"Connecting to added (box)…", "✓ connected in ", "✕ restore after restart: no systemd user manager", "Agents:", "--host added", "added is ready."} {
		if !strings.Contains(out, want) {
			t.Errorf("add lacks %q:\n%s", want, out)
		}
	}
	if h, err := client.FindHost("added"); err != nil || h.Machine == nil {
		t.Errorf("the host was not saved: %+v, %v", h, err)
	}
	fails(t, "cannot name a host", "host", "add", "bad name")
}
