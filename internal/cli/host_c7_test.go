package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/provider"
)

func TestHostSetTrustedIdleLabelsAndWarnings(t *testing.T) {
	newTestHost(t)
	ctx := context.Background()

	// Raising trust with input off and no --confirm fails with exit code 4.
	got, code := lineErr(t, "host", "set", "box", "trusted=true", "--no-input")
	if code != 4 || !strings.Contains(got, "--confirm box") {
		t.Fatalf("host set trusted=true --no-input: exit %d\n%s", code, got)
	}

	// Wrong --confirm fails with exit code 2.
	if got, code := lineErr(t, "host", "set", "box", "trusted=true", "--confirm", "other"); code != 2 || !strings.Contains(got, "does not match") {
		t.Fatalf("host set --confirm wrong: exit %d\n%s", code, got)
	}

	// --confirm without trusted=true fails with exit code 2.
	if got, code := lineErr(t, "host", "set", "box", "trusted=false", "--confirm", "box"); code != 2 || !strings.Contains(got, "--confirm applies only") {
		t.Fatalf("host set --confirm without trusted=true: exit %d\n%s", code, got)
	}

	// Bad trusted value fails with exit code 2.
	if got, code := lineErr(t, "host", "set", "box", "trusted=maybe"); code != 2 || !strings.Contains(got, "true or false") {
		t.Fatalf("host set trusted=maybe: exit %d\n%s", code, got)
	}

	// Unknown key suggests trusted / idle / floor.
	if got, code := lineErr(t, "host", "set", "box", "trust=true"); code != 2 || !strings.Contains(got, `Did you mean "trusted"?`) {
		t.Fatalf("host set trust=true: exit %d\n%s", code, got)
	}

	// Raising trust interactively: Enter keeps untrusted and cancels (exit 130).
	var promptOut bytes.Buffer
	err := runHostSet(ctx, nil, testPrompts("\n", &promptOut), "box", []string{"trusted=true"}, "")
	if !isCancel(err) {
		t.Fatalf("trusted=true Enter = %v, want cancel", err)
	}
	if !strings.Contains(promptOut.String(), "Type box to mark it trusted, or press Enter to keep it untrusted:") {
		t.Fatalf("trusted prompt = %q", promptOut.String())
	}
	if h, _ := client.FindHost("box"); h.Trusted {
		t.Fatal("Enter marked host trusted")
	}

	// Raising trust interactively with exact typed name succeeds.
	promptOut.Reset()
	errOut, err := said(t, func() error {
		return runHostSet(ctx, nil, testPrompts("box\n", &promptOut), "box", []string{"trusted=true"}, "")
	})
	if err != nil || !strings.Contains(errOut, "box: trusted false → true.") {
		t.Fatalf("trusted=true typed name: err=%v stderr=%q", err, errOut)
	}
	if h, _ := client.FindHost("box"); !h.Trusted {
		t.Fatal("typed name did not save trusted=true")
	}

	// Lowering trust works immediately without confirmation.
	if msg := okSaid(t, "host", "set", "box", "trusted=false"); !strings.Contains(msg, "box: trusted true → false.") {
		t.Fatalf("host set trusted=false = %q", msg)
	}
	if h, _ := client.FindHost("box"); h.Trusted {
		t.Fatal("trusted=false did not save")
	}

	// Raising trust non-interactively with --confirm works.
	if msg := okSaid(t, "host", "set", "box", "trusted=true", "--confirm", "box", "--no-input"); !strings.Contains(msg, "box: trusted false → true.") {
		t.Fatalf("host set trusted=true --confirm = %q", msg)
	}

	// Labels and idle in one call.
	msg := okSaid(t, "host", "set", "box", "idle=2h", "label.env=prod", "label.team=core")
	for _, want := range []string{"box: idle 2h.", "box: label.env prod.", "box: label.team core."} {
		if !strings.Contains(msg, want) {
			t.Errorf("host set multi lacks %q:\n%s", want, msg)
		}
	}
	h, _ := client.FindHost("box")
	if h.Machine.IdleStop != "2h" || h.Labels["env"] != "prod" || h.Labels["team"] != "core" {
		t.Fatalf("saved host after multi set = %+v", h)
	}
	// Removing a label with empty value.
	if msg := okSaid(t, "host", "set", "box", "label.team="); !strings.Contains(msg, "box: removed label.team.") {
		t.Fatalf("remove label = %q", msg)
	}
	if h, _ := client.FindHost("box"); h.Labels["team"] != "" || h.Labels["env"] != "prod" {
		t.Fatalf("labels after remove = %v", h.Labels)
	}

	// idle on a non-created host fails with exit code 2 (cli-spec 11.1, rule 3).
	if err := saveHost(ctx, client.Host{Name: "plain", SSH: "box"}); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "set", "plain", "idle=30m"); code != 2 || !strings.Contains(got, "was not made by `host create`") {
		t.Fatalf("host set idle on plain host: exit %d\n%s", code, got)
	}

	// Busy lock on host fails fast with host_busy.
	unlock, err := client.LockHost(ctx, "box", "host rm")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if got, code := lineErr(t, "host", "set", "box", "trusted=false"); code != 1 || !strings.Contains(got, "box is busy: host rm changes it") {
		t.Fatalf("host set while locked: exit %d\n%s", code, got)
	}
}

type failingRunTransport struct {
	client.Transport
	err error
}

func (f failingRunTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, f.err
}

func TestHostSetPartialFailureOnRemoteIdle(t *testing.T) {
	_, tr := withFake(t, nil)
	ctx := context.Background()
	// Register a fake provider named "lima" so powersOffWhenIdle("lima") is true
	// and applyIdle runs over the failing transport.
	m := clienttest.NewMachine(client.MachineRunning, failingRunTransport{Transport: tr, err: errors.New("sudo poweroff denied")})
	client.RegisterProvider("lima", func(client.Host) (client.Lifecycle, error) { return m, nil })
	t.Cleanup(func() {
		for _, p := range providers {
			if p.Name() == "lima" {
				client.RegisterProvider(p.Name(), p.Open)
			}
		}
	})

	if err := saveHost(ctx, client.Host{
		Name:    "vm",
		Machine: &client.Machine{Provider: "lima", ID: "lima-vm"},
	}); err != nil {
		t.Fatal(err)
	}
	got, code := lineErr(t, "host", "set", "vm", "label.env=stage", "idle=2h")
	if code != 1 || !strings.Contains(got, "already saved: label.env=stage") {
		t.Fatalf("partial failure: exit %d\n%s", code, got)
	}
	if h, _ := client.FindHost("vm"); h.Labels["env"] != "stage" {
		t.Fatalf("local setting was not saved before remote failure: %+v", h)
	}
}

type failingStatusLifecycle struct {
	client.Lifecycle
	statusErr error
}

func (f failingStatusLifecycle) Status(context.Context) (string, error) { return "", f.statusErr }

func TestHostRmAllPathsAndRefusals(t *testing.T) {
	m, tr := withFake(t, []client.Session{
		{ID: "abcd2345", Name: "one", State: api.StateIdle, Alive: true},
		{ID: "bcde3456", Name: "two", State: api.StateIdle, Alive: true},
	})
	ctx := context.Background()
	box := client.Host{
		Name:    "box",
		Machine: &client.Machine{Provider: "fake", ID: "fake-box", Created: "2026-11-28T00:00:00Z"},
	}
	if err := saveHost(ctx, box); err != nil {
		t.Fatal(err)
	}

	// --confirm or --force without --destroy fail with exit code 2.
	if got, code := lineErr(t, "host", "rm", "box", "--confirm", "box"); code != 2 || !strings.Contains(got, "--confirm needs --destroy") {
		t.Fatalf("--confirm without --destroy: exit %d\n%s", code, got)
	}
	if got, code := lineErr(t, "host", "rm", "box", "--force"); code != 2 || !strings.Contains(got, "--force needs --destroy") {
		t.Fatalf("--force without --destroy: exit %d\n%s", code, got)
	}
	// Wrong --confirm fails with exit code 2.
	if got, code := lineErr(t, "host", "rm", "box", "--destroy", "--confirm", "other"); code != 2 || !strings.Contains(got, "does not match") {
		t.Fatalf("wrong --confirm: exit %d\n%s", code, got)
	}
	// Unknown host with --confirm rewrites both host and --confirm in Try:.
	if got, code := lineErr(t, "host", "rm", "bo", "--destroy", "--confirm", "bo"); code != 3 || !strings.Contains(got, "Try: "+brand.Name+" host rm box --destroy --confirm box") {
		t.Fatalf("unknown host with --confirm: exit %d\n%s", code, got)
	}
	// --destroy on a host without a machine fails with exit code 2.
	if err := saveHost(ctx, client.Host{Name: "plain", SSH: "me@plain"}); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "rm", "plain", "--destroy"); code != 2 || !strings.Contains(got, "no machine to delete") || !strings.Contains(got, "Try: "+brand.Name+" host rm plain") {
		t.Fatalf("--destroy on plain host: exit %d\n%s", code, got)
	}

	// --destroy without --confirm with input off fails with exit code 4.
	if got, code := lineErr(t, "host", "rm", "box", "--destroy", "--no-input"); code != 4 || !strings.Contains(got, "--confirm box") {
		t.Fatalf("--destroy --no-input: exit %d\n%s", code, got)
	}

	// Running machine whose session list fails MUST refuse --destroy (cli-spec 11.3, rule 9).
	tr.Fail("ls", &proto.Error{Code: "error", Message: "boom"})
	if got, code := lineErr(t, "host", "rm", "box", "--destroy", "--confirm", "box"); code != 1 || !strings.Contains(got, "cannot list sessions") || !strings.Contains(got, "--force") {
		t.Fatalf("failed ls should refuse --destroy: exit %d\n%s", code, got)
	}
	if slices.Contains(m.Calls(), "destroy") {
		t.Fatal("destroyed machine when ls failed")
	}
	tr.Fail("ls", nil)

	// Provider whose Status fails MUST refuse --destroy unless --force.
	client.RegisterProvider("broken", func(client.Host) (client.Lifecycle, error) {
		return failingStatusLifecycle{Lifecycle: m, statusErr: errors.New("provider offline")}, nil
	})
	if err := saveHost(ctx, client.Host{Name: "brk", Machine: &client.Machine{Provider: "broken", ID: "b1"}}); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "rm", "brk", "--destroy", "--confirm", "brk"); code != 1 || !strings.Contains(got, "cannot check whether brk is running") {
		t.Fatalf("failed Status should refuse --destroy: exit %d\n%s", code, got)
	}
	_ = client.ForgetHost(ctx, "brk")

	// Interactive host rm on created host shows session count and asks typed name.
	// Wrong name cancels (exit 130) and changes nothing.
	var out bytes.Buffer
	err := rmHost(ctx, testPrompts("nope\n", &out), "box", hostRmOptions{})
	if !isCancel(err) {
		t.Fatalf("wrong name = %v, want cancel", err)
	}
	if !strings.Contains(out.String(), "It holds 2 sessions.") || !strings.Contains(out.String(), "Type box to delete the machine too, or press Enter to keep it:") {
		t.Fatalf("interactive rm prompt:\n%s", out.String())
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("wrong name forgot host")
	}

	// Enter keeps the machine and forgets the host.
	out.Reset()
	if err := rmHost(ctx, testPrompts("\n", &out), "box", hostRmOptions{}); err != nil {
		t.Fatalf("rm Enter = %v", err)
	}
	if slices.Contains(m.Calls(), "destroy") {
		t.Fatal("Enter destroyed the machine")
	}
	if !strings.Contains(out.String(), "Forgot box.") || !strings.Contains(out.String(), "Its fake machine fake-box is kept.") {
		t.Fatalf("rm Enter output:\n%s", out.String())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("Enter did not forget box")
	}

	// Re-save box and confirm exact typed name destroys and forgets it.
	if err := saveHost(ctx, box); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := rmHost(ctx, testPrompts("box\n", &out), "box", hostRmOptions{}); err != nil {
		t.Fatalf("rm typed name = %v", err)
	}
	if !slices.Contains(m.Calls(), "destroy") {
		t.Fatalf("typed name did not destroy: %v", m.Calls())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("box still saved after destroy")
	}
	if _, ok, _ := client.LoadRemovalRecord("box"); ok {
		t.Fatal("removal record was left behind after forget")
	}
}

func TestHostUpgradeUnchangedInstalledAndNoPrune(t *testing.T) {
	_, tr := withFake(t, nil)
	ctx := context.Background()
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	if err := saveHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	// 1. Helper already installed -> unchanged, text & --json.
	errOut := okSaid(t, "host", "upgrade", "box")
	if !strings.Contains(errOut, "box has helper "+client.HelperVersion()+" already.") {
		t.Fatalf("host upgrade unchanged stderr = %q", errOut)
	}
	rawJSON := okJSON(t, "host", "upgrade", "box", "--json")
	checkJSON(t, "host upgrade box --json", rawJSON, false)
	var res map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &res); err != nil {
		t.Fatal(err)
	}
	if res["host"] != "box" || res["helper"] != client.HelperVersion() || res["outcome"] != "unchanged" {
		t.Fatalf("host upgrade --json = %+v", res)
	}

	// 2. host show when helper is missing must NOT install it and must hint `host upgrade`.
	emptyHome := t.TempDir()
	client.RegisterProvider("emptysh", func(client.Host) (client.Lifecycle, error) {
		return &localShellLifecycle{home: emptyHome}, nil
	})
	if err := saveHost(ctx, client.Host{Name: "nohelper", Machine: &client.Machine{Provider: "emptysh", ID: "empty-1"}}); err != nil {
		t.Fatal(err)
	}
	showOut, showHints, err := runApart(t, "host", "show", "nohelper")
	if err != nil {
		t.Fatalf("host show with missing helper: %v", err)
	}
	if !strings.Contains(showOut, "nohelper did not answer:") || !strings.Contains(showHints, "Try: "+brand.Name+" host upgrade nohelper") {
		t.Fatalf("host show missing helper:\nstdout:\n%s\nstderr:\n%s", showOut, showHints)
	}
	if _, err := os.Stat(filepath.Join(emptyHome, ".local", "share", brand.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host show installed helper into %s: %v", emptyHome, err)
	}

	// 3. host upgrade when helper is missing installs without pruning.
	tr.NoHelper = true
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	out, errOut, err := runApart(t, "host", "upgrade", "box", "--json")
	if err != nil {
		t.Fatalf("host upgrade install: %v\nstderr: %s", err, errOut)
	}
	if errOut != "" {
		t.Errorf("host upgrade --json wrote non-empty stderr: %q", errOut)
	}
	checkJSON(t, "host upgrade install --json", out, false)
	if err := json.Unmarshal([]byte(out), &res); err != nil || res["outcome"] != "installed" {
		t.Fatalf("host upgrade install --json = %s, err=%v", out, err)
	}
	for _, r := range tr.Remotes() {
		if strings.Contains(r, " prune") {
			t.Fatalf("host upgrade ran prune: %q", r)
		}
	}
}

func TestHostUpgradeReinstallsOnDigestMismatchWithoutPrune(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("embedded linux helper test requires Linux")
	}
	bin, ok := helpers.For("linux", runtime.GOARCH)
	if !ok || len(bin) == 0 {
		t.Skip("this build has no embedded helper for linux/" + runtime.GOARCH)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// Use a real shell transport backed by a temporary HOME and real embedded helper.
	home := t.TempDir()
	ver := client.HelperVersion()
	dir := filepath.Join(home, ".local", "share", brand.Name, "bin", ver)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(dir, brand.HostBinary)
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Also put a newer helper version alongside to prove host upgrade leaves it alone.
	otherDir := filepath.Join(home, ".local", "share", brand.Name, "bin", "9.9.9-future")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(otherDir, brand.HostBinary)
	if err := os.WriteFile(otherFile, []byte("newer"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Register a minimal shell-based provider pointing HOME to our temp dir.
	oldProviders := providers
	client.RegisterProvider("localsh", func(client.Host) (client.Lifecycle, error) {
		return &localShellLifecycle{home: home}, nil
	})
	t.Cleanup(func() { providers = oldProviders })

	ctx := context.Background()
	h := client.Host{Name: "tampered", Machine: &client.Machine{Provider: "localsh", ID: "local-1"}}
	if err := saveHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	out, _, err := runApart(t, "host", "upgrade", "tampered", "--json")
	if err != nil {
		t.Fatalf("host upgrade digest_mismatch: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil || res["outcome"] != "reinstalled" {
		t.Fatalf("host upgrade --json = %s, err=%v", out, err)
	}
	if _, err := os.Stat(otherFile); err != nil {
		t.Fatalf("host upgrade pruned newer helper %s: %v", otherFile, err)
	}
}

type localShellLifecycle struct{ home string }

func (localShellLifecycle) Status(context.Context) (string, error) { return client.MachineRunning, nil }
func (localShellLifecycle) Start(context.Context) error            { return nil }
func (localShellLifecycle) Stop(context.Context) error             { return nil }
func (localShellLifecycle) Destroy(context.Context) error          { return nil }
func (l localShellLifecycle) Transport(context.Context, bool) (client.Transport, error) {
	return &localShellTransport{home: l.home}, nil
}

type localShellTransport struct{ home string }

func (s *localShellTransport) env() []string {
	env := slices.Clone(os.Environ())
	for i, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			env[i] = "HOME=" + s.home
			return env
		}
	}
	return append(env, "HOME="+s.home)
}

func (s *localShellTransport) Start(ctx context.Context, remote string) (client.Proc, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	cmd.Env = s.env()
	return client.CommandProc(cmd, nil)
}

func (s *localShellTransport) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	cmd.Env = s.env()
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (s *localShellTransport) ReachHint() string { return "check local shell host" }

func TestHostShowJSONMatchesSharedDetails(t *testing.T) {
	newTestHost(t)
	// Pin free disk space so two separate host show calls (text and --json)
	// observe the same snapshot even when concurrent tests or the OS change
	// available blocks on the underlying file system between calls.
	oldDiskFree := host.DiskFree
	host.DiskFree = func(string) int64 { return 50 << 30 }
	t.Cleanup(func() { host.DiskFree = oldDiskFree })
	newSession(t, "s1", "sleep", "60")
	newSession(t, "s2", "sleep", "60")

	showOut, _, err := runApart(t, "host", "show", "box")
	if err != nil {
		t.Fatalf("host show: %v", err)
	}
	showJSON := okJSON(t, "host", "show", "box", "--json")
	checkJSON(t, "host show box --json", showJSON, false)

	var obj struct {
		Name         string           `json:"name"`
		Trusted      bool             `json:"trusted"`
		Reachable    bool             `json:"reachable"`
		SessionCount int              `json:"session_count"`
		Info         *client.HostInfo `json:"info"`
	}
	if err := json.Unmarshal([]byte(showJSON), &obj); err != nil {
		t.Fatal(err)
	}
	h, err := client.FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	if !obj.Reachable || obj.SessionCount != 2 || obj.Info == nil || obj.Trusted != h.Trusted {
		t.Fatalf("unexpected host show --json: %+v", obj)
	}
	if len(obj.Info.AgentChecks) != 0 {
		t.Errorf("host show --json included agent_checks: %+v", obj.Info.AgentChecks)
	}
	det := client.BuildHostDetails(h, obj.Info, obj.SessionCount)
	for _, want := range []string{det.System, det.Helper, det.Limits, "Trusted     no"} {
		if !strings.Contains(showOut, want) {
			t.Errorf("host show text lacks shared field %q:\n%s", want, showOut)
		}
	}
}

func TestHostC7Synonyms(t *testing.T) {
	newTestHost(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"update", "box"}, `Did you mean "host upgrade"?`},
		{[]string{"upgrade", "box"}, `Did you mean "host upgrade"?`},
		{[]string{"host", "remove", "box"}, `Did you mean "rm"?`},
		{[]string{"host", "delete", "box"}, `Did you mean "rm"?`},
		{[]string{"host", "forget", "box"}, `Did you mean "rm"?`},
		{[]string{"host", "config", "box"}, `Did you mean "set"?`},
		{[]string{"host", "settings", "box"}, `Did you mean "set"?`},
		{[]string{"host", "details", "box"}, `Did you mean "show"?`},
	} {
		got, code := lineErr(t, tc.args...)
		if code != 2 || !strings.Contains(got, tc.want) {
			t.Errorf("%v: exit %d, want %q:\n%s", tc.args, code, tc.want, got)
		}
	}
	_ = provider.Request{}
}
