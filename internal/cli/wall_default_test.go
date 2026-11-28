package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/onboard"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func saveBoxHost(t *testing.T, th *testHost, fn func(*client.Host)) {
	t.Helper()
	h := th.ch
	h.WallFloor = api.LevelNone
	fn(&h)
	th.ch = h
	if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
		t.Fatal(err)
	}
}

func TestNewWallFloorRefusalAndDowngrade(t *testing.T) {
	th := newTestHost(t)

	// 1. A host that can run only none refuses new without --downgrade with exit 5 (Q6).
	got, code := lineErr(t, "new", "box", "--detach", "--", "sleep", "60")
	if code != 5 || !strings.Contains(got, "can run only wall") || !strings.Contains(got, "--wall none --downgrade") {
		t.Fatalf("none-only host without --downgrade = exit %d:\n%s", code, got)
	}

	// Explicit --wall none on a none-floor host still needs --downgrade.
	got, code = lineErr(t, "new", "box", "--detach", "--wall", "none", "--", "sleep", "60")
	if code != 5 || !strings.Contains(got, "--wall none --downgrade") {
		t.Fatalf("explicit --wall none without --downgrade = exit %d:\n%s", code, got)
	}

	// With --downgrade, it starts unwalled, records downgraded_from="default",
	// shows [!] in ls and (! downgraded) in show.
	out := okJSON(t, "new", "box", "--detach", "--json", "--wall", "none", "--downgrade", "--name", "dw-none", "--", "sleep", "60")
	var sess client.Session
	if err := json.Unmarshal([]byte(out), &sess); err != nil {
		t.Fatalf("decode new --json: %v\n%s", err, out)
	}
	if sess.Wall == nil || sess.Wall.Level != api.LevelNone || sess.Wall.Driver != api.LevelNone || sess.Wall.DowngradedFrom != "default" {
		t.Fatalf("downgraded session wall = %+v", sess.Wall)
	}
	if lsOut := ok(t, "ls", "box"); !strings.Contains(lsOut, "[!]") {
		t.Errorf("ls missing [!] on downgraded session:\n%s", lsOut)
	}
	if showOut := ok(t, "show", "box/"+sess.ID); !strings.Contains(showOut, "none (! downgraded)") {
		t.Errorf("show missing downgraded info:\n%s", showOut)
	}
	okSaid(t, "rm", "box/"+sess.ID, "-y")

	// 2. Host with floor=gvisor refuses --wall process without --downgrade (exit 5),
	// and records floor=gvisor / downgraded_from=gvisor on --wall none --downgrade.
	saveBoxHost(t, th, func(h *client.Host) { h.WallFloor = api.LevelGVisor })
	got, code = lineErr(t, "new", "box", "--detach", "--wall", "process", "--", "sleep", "60")
	if code != 5 || !strings.Contains(got, "gvisor or stronger") || !strings.Contains(got, "host set box floor=process") {
		t.Fatalf("below gvisor floor = exit %d:\n%s", code, got)
	}
	out = okJSON(t, "new", "box", "--detach", "--json", "--wall", "none", "--downgrade", "--name", "dw-gv", "--", "sleep", "60")
	sess = client.Session{}
	if err := json.Unmarshal([]byte(out), &sess); err != nil {
		t.Fatal(err)
	}
	if sess.Wall == nil || sess.Wall.Floor != api.LevelGVisor || sess.Wall.DowngradedFrom != api.LevelGVisor {
		t.Fatalf("gvisor-floor downgrade wall = %+v", sess.Wall)
	}
	if showOut := ok(t, "show", "box/"+sess.ID); !strings.Contains(showOut, "none (! below the floor gvisor)") {
		t.Errorf("show missing below-floor detail:\n%s", showOut)
	}
	// Opening `shell` on an unwalled session must also record the downgrade (G9).
	shellOut := okJSON(t, "shell", "box/"+sess.ID, "--detach", "--json", "--name", "dw-gv-sh")
	var shSess client.Session
	if err := json.Unmarshal([]byte(shellOut), &shSess); err != nil {
		t.Fatalf("decode shell --json: %v\n%s", err, shellOut)
	}
	if shSess.Wall == nil || shSess.Wall.Level != api.LevelNone || shSess.Wall.Floor != api.LevelGVisor || shSess.Wall.DowngradedFrom != api.LevelGVisor {
		t.Fatalf("shell on unwalled session wall = %+v, want none/gvisor/gvisor", shSess.Wall)
	}
	if shShow := ok(t, "show", "box/"+shSess.ID); !strings.Contains(shShow, "none (! below the floor gvisor)") {
		t.Errorf("shell show missing below-floor detail:\n%s", shShow)
	}
	okSaid(t, "rm", "box/"+shSess.ID, "-y")
	okSaid(t, "rm", "box/"+sess.ID, "-y")

	// 3. Shared host with floor=process refuses default new without --downgrade (Q5).
	saveBoxHost(t, th, func(h *client.Host) {
		h.WallFloor = api.LevelProcess
		h.Labels = map[string]string{"tier": "shared"}
	})
	got, code = lineErr(t, "new", "box", "--detach", "--", "sleep", "60")
	if code != 5 || !strings.Contains(got, "shared host") || !strings.Contains(got, "--wall process --downgrade") {
		t.Fatalf("shared host process floor = exit %d:\n%s", code, got)
	}
	out = okJSON(t, "new", "box", "--detach", "--json", "--wall", "none", "--downgrade", "--name", "dw-shared", "--", "sleep", "60")
	sess = client.Session{}
	if err := json.Unmarshal([]byte(out), &sess); err != nil {
		t.Fatal(err)
	}
	if sess.Wall == nil || sess.Wall.DowngradedFrom != api.LevelGVisor {
		t.Fatalf("shared host downgrade wall = %+v, want downgraded_from=gvisor", sess.Wall)
	}
	okSaid(t, "rm", "box/"+sess.ID, "-y")

	// 4. --in cannot combine with --downgrade (exit 2, flags_conflict).
	got, code = lineErr(t, "new", "box", "--in", "abc", "--downgrade", "--detach")
	if code != 2 || !strings.Contains(got, "--downgrade cannot be used with --in") {
		t.Fatalf("--in + --downgrade = exit %d:\n%s", code, got)
	}
}

func TestConfigCheckSandboxWallAndConfigRaising(t *testing.T) {
	th := newTestHost(t)

	// config check accepts valid sandbox.wall values in config.toml and hosts.toml.
	writeSettings(t, "[sandbox]\nwall = \"auto\"\n")
	ok(t, "config", "check")

	// config check rejects invalid sandbox.wall with suggestions.
	writeSettings(t, "[sandbox]\nwall = \"procss\"\n")
	out, err := run(t, "config", "check")
	if err == nil || !strings.Contains(out, "sandbox.wall must be") || !strings.Contains(out, "process") {
		t.Fatalf("invalid config sandbox.wall: err=%v\n%s", err, out)
	}

	// Valid config layer below host floor raises to floor (Q90) and emits a notice.
	writeSettings(t, "[sandbox]\nwall = \"none\"\n")
	saveBoxHost(t, th, func(h *client.Host) {
		h.WallFloor = api.LevelMicroVM
	})
	// Starting will raise target to microvm and fail at host wall_unavailable,
	// after printing the wall_raised notice on stderr (JSON warning when --json).
	_, errOut, err := runApart(t, "new", "box", "--detach", "--json", "--", "sleep", "60")
	if err == nil {
		t.Fatal("expected microvm wall_unavailable error")
	}
	if !strings.Contains(errOut, `"code":"wall_raised"`) || !strings.Contains(errOut, "below box's floor microvm") {
		t.Fatalf("expected JSON wall_raised warning on stderr, got:\n%s", errOut)
	}
}

func TestDowngradedSessionFiresWallDowngradedAlert(t *testing.T) {
	th := newTestHost(t)
	saveBoxHost(t, th, func(h *client.Host) { h.WallFloor = api.LevelProcess })

	logFile := filepath.Join(t.TempDir(), "alerts.jsonl")
	ok(t, "host", "alerts", "set", "box", "--command", "cat >> '"+logFile+"'")

	out := okJSON(t, "new", "box", "--detach", "--json", "--wall", "none", "--downgrade", "--name", "alert-dw", "--", "sleep", "60")
	var sess client.Session
	if err := json.Unmarshal([]byte(out), &sess); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _, _ = runApart(t, "rm", "box/"+sess.ID, "-y") }()

	hosttest.WaitFor(t, 10*time.Second, "wall_downgraded alert", func() bool {
		b, err := os.ReadFile(logFile)
		return err == nil && strings.Contains(string(b), `"event":"`+alerts.WallDowngraded+`"`)
	})
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "started below its wall floor (none, below process)") {
		t.Fatalf("alert log missing downgrade summary:\n%s", string(b))
	}
}

func TestAgentsInstallAndLoginRecordDowngrade(t *testing.T) {
	th := newTestHost(t)
	saveBoxHost(t, th, func(h *client.Host) { h.WallFloor = api.LevelProcess })

	claude, okFind := agents.Find("claude")
	if !okFind {
		t.Fatal("claude not in agent table")
	}
	instArgs := onboard.InstallArgs(claude)
	if instArgs.Wall != api.LevelNone || !instArgs.Downgrade {
		t.Fatalf("InstallArgs = %+v, want wall=none downgrade=true", instArgs)
	}
	loginArgs, hasLogin := onboard.LoginArgs(claude)
	if !hasLogin || loginArgs.Wall != api.LevelNone || !loginArgs.Downgrade {
		t.Fatalf("LoginArgs = %+v (ok=%v), want wall=none downgrade=true", loginArgs, hasLogin)
	}

	// Start a login-style session through applyNoneDowngrade + host.New and
	// verify manifest downgrade recording and ls [!] badge without --downgrade.
	logFile := filepath.Join(t.TempDir(), "login-alerts.jsonl")
	ok(t, "host", "alerts", "set", "box", "--command", "cat >> '"+logFile+"'")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, th.ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	args := client.TerminalArgs(client.NewArgs{
		Name: "login-claude",
		Cwd:  "~",
		Argv: []string{"/bin/sh", "-c", "sleep 60"},
	}, 80, 24)
	applyNoneDowngrade(ctx, conn, th.ch, &args)
	s, err := conn.NewSession(ctx, args)
	if err != nil {
		t.Fatalf("NewSession login: %v", err)
	}
	defer func() { _, _, _ = runApart(t, "rm", "box/"+s.ID, "-y") }()

	if s.Wall == nil || s.Wall.Level != api.LevelNone || s.Wall.Driver != api.LevelNone || s.Wall.Floor != api.LevelProcess || s.Wall.DowngradedFrom != api.LevelProcess {
		t.Fatalf("login session wall = %+v", s.Wall)
	}
	if lsOut := ok(t, "ls", "box"); !strings.Contains(lsOut, "[!]") {
		t.Errorf("ls missing [!] on login session:\n%s", lsOut)
	}
	hosttest.WaitFor(t, 10*time.Second, "login wall_downgraded alert", func() bool {
		b, err := os.ReadFile(logFile)
		return err == nil && strings.Contains(string(b), `"event":"`+alerts.WallDowngraded+`"`)
	})
}

func TestNewDefaultsToProcessWallOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process wall requires Linux")
	}
	pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	probe := process.Driver{}.Probe(pctx)
	cancel()
	if !probe.OK {
		t.Skipf("bubblewrap process wall unavailable: %s", probe.Reason)
	}

	th := newTestHost(t)

	// 1. Pinned wall_floor="process": default `new` starts a process-walled session without --wall.
	saveBoxHost(t, th, func(h *client.Host) { h.WallFloor = api.LevelProcess })
	out, errOut, err := runApart(t, "new", "box", "--detach", "--json", "--name", "walled-default", "--", "/bin/sh", "-c", "sleep 60")
	if err != nil {
		t.Fatalf("default new on process-floor host failed: %v\nstderr: %s", err, errOut)
	}
	if errOut != "" {
		t.Errorf("unexpected stderr on pinned host: %q", errOut)
	}
	var s client.Session
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, out)
	}
	if s.Wall == nil || s.Wall.Level != api.LevelProcess || s.Wall.Driver != api.LevelProcess || s.Wall.Floor != api.LevelProcess || s.Wall.DowngradedFrom != "" {
		t.Fatalf("default walled session = %+v", s.Wall)
	}
	m, err := host.LoadManifest(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Wall == nil || m.Wall.PackageManagers != api.DefaultPackageManagers || m.Wall.MinReleaseAge != api.DefaultMinReleaseAge {
		t.Fatalf("default package manager settings = %+v, want %s/%s", m.Wall, api.DefaultPackageManagers, api.DefaultMinReleaseAge)
	}
	okSaid(t, "rm", "box/"+s.ID, "-y")

	// 2. Unpinned host (wall_floor=""): probes, runs at process without pinning, and prints a notice.
	saveBoxHost(t, th, func(h *client.Host) { h.WallFloor = "" })
	out, errOut, err = runApart(t, "new", "box", "--detach", "--name", "unpinned-default", "--", "/bin/sh", "-c", "sleep 60")
	if err != nil {
		t.Fatalf("unpinned new failed: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if !strings.Contains(errOut, "box has no wall floor yet") || !strings.Contains(errOut, brand.Name+" host add box") {
		t.Errorf("unpinned host stderr missing notice:\n%s", errOut)
	}
	savedHost, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	if savedHost.WallFloor != "" {
		t.Errorf("unpinned start pinned WallFloor = %q, want empty", savedHost.WallFloor)
	}
	match := startedRE.FindStringSubmatch(errOut)
	if match == nil {
		t.Fatalf("unpinned stderr missing Started line:\n%s", errOut)
	}
	m2, err := host.LoadManifest(match[1])
	if err != nil {
		t.Fatal(err)
	}
	if m2.Wall == nil || m2.Wall.Level != api.LevelProcess {
		t.Fatalf("unpinned session wall = %+v, want process", m2.Wall)
	}
	okSaid(t, "rm", "box/"+match[1], "-y")
}
