package lima

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// testdata/list.ndjson is `limactl list --json` from Lima 2.2.0 on macOS
// (one running instance, recorded; one stopped, edited from it), with the
// home folder renamed. The recording was cut down: Lima 2.2.0 also prints
// each instance's config (pkg/limatype/lima_instance.go), which it leaves
// out. The fields are defined in pkg/store/instance.go of lima-vm/lima.
func TestParseList(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "list.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseList(b, provider.MachineName("ltest"))
	if err != nil || in == nil {
		t.Fatalf("parse: %v, %v", in, err)
	}
	if in.Status != "Running" || in.SSHConfigFile != "/Users/me/.lima/"+provider.MachineName("ltest")+"/ssh.config" || in.CPUs != 2 || in.Memory != 4<<30 || in.Disk != 40<<30 ||
		in.VMType != "vz" || in.Dir != "/Users/me/.lima/"+provider.MachineName("ltest") || in.Config != nil {
		t.Fatalf("instance = %+v", in)
	}
	other, _ := parseList(b, "other")
	if other == nil || state(other.Status) != client.MachineStopped {
		t.Fatalf("other = %+v", other)
	}
	if none, err := parseList(b, "nope"); none != nil || err != nil {
		t.Fatalf("a missing instance: %v, %v", none, err)
	}
	if none, err := parseList(nil, "nope"); none != nil || err != nil {
		t.Fatalf("no instances: %v, %v", none, err)
	}
}

func TestState(t *testing.T) {
	for in, want := range map[string]string{
		"Running": client.MachineRunning, "Stopped": client.MachineStopped, "Broken": client.MachineStopped,
		"Installing": client.MachineStarting, "": client.MachineStarting,
	} {
		if got := state(in); got != want {
			t.Errorf("state(%q) = %s, want %s", in, got, want)
		}
	}
}

// testdata/create-<os>.args are the arguments that make a VM, one to a line.
func TestCreateArgs(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		b, err := os.ReadFile(filepath.Join("testdata", "create-"+goos+".args"))
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		got := CreateArgs("vm", provider.Request{}, goos)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: args =\n%q\nwant\n%q", goos, got, want)
		}
	}
	got := strings.Join(CreateArgs("vm", provider.Request{CPUs: 4, MemoryGiB: 8, DiskGiB: 80}, "darwin"), " ")
	if !strings.Contains(got, "--cpus=4 --memory=8 --disk=80") {
		t.Fatal(got)
	}
}

// shellLine quotes as client.ShellQuote does, so a backtick and a dollar
// sign, which a shell would expand, are quoted too.
func TestShellLine(t *testing.T) {
	got := shellLine([]string{"edit", "--set", ".nestedVirtualization = false", "it's", "a`date`", "$HOME", "vm"})
	if want := `edit --set '.nestedVirtualization = false' 'it'\''s' 'a` + "`date`" + `' '$HOME' vm`; got != want {
		t.Fatalf("shellLine = %s, want %s", got, want)
	}
}

// fakeLimactl acts as limactl for one Mac or Linux machine. It keeps each
// instance's status, and whether its VM asks for nested virtualisation, in
// files. A start writes ha.pid, as Lima's host agent does, and a stop
// removes it. It logs each call as one line: its arguments, each followed
// by the unit separator, so that the test sees every argument whole.
//
// Files in FAKE_DIR change what it does:
//   - refuse-nesting: this Mac cannot run a VM inside a VM. Every start of
//     a VM that asks for nesting fails, as Lima's does: refuse-stderr goes
//     to stderr, and refuse-ha becomes the instance's ha.stderr.log. With
//     refuse-late, the log is written after the start returns, by a process
//     whose PID is in ha.pid. It removes ha.pid first, as Lima's host agent
//     does.
//   - fail-start: the next start fails: fail-start goes to stderr, and
//     fail-ha becomes the instance's ha.stderr.log.
//   - override-open, override-qemu and override-nested: Lima's
//     override.yaml sets hostLoopback true, vmType qemu, or
//     nestedVirtualization true, whatever create or edit asked for.
//   - edit-fails, delete-fails, version-fails and version-fails-quiet: those
//     commands fail. version holds what --version prints.
//   - after-edit-list-fails, after-edit-list-bad,
//     after-edit-list-incomplete, after-edit-list-empty and
//     after-edit-list-other: once an edit has run, list fails, prints text
//     that is not JSON, leaves out nestedVirtualization, lists nothing, or
//     lists only another instance.
//   - networks-json: the config's networks, as JSON.
//
// Lines that start with # in the copied files are the testdata's notes,
// which limactl never prints.
const fakeLimactl = `#!/bin/sh
for a; do printf '%s\037' "$a"; done >> "$FAKE_DIR/log"
printf '\n' >> "$FAKE_DIR/log"
cmd=$1; shift
for a; do n=$a; done
copy() { grep -v '^#' "$1"; true; }
case $cmd in
--version)
  if [ -f "$FAKE_DIR/version-fails" ]; then echo "limactl: command failed" >&2; exit 1; fi
  if [ -f "$FAKE_DIR/version-fails-quiet" ]; then exit 3; fi
  if [ -f "$FAKE_DIR/version" ]; then cat "$FAKE_DIR/version"; else echo "limactl version 2.3.0"; fi ;;
list)
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-fails" ]; then echo "fake limactl: list failed" >&2; exit 1; fi
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-bad" ]; then echo '{"name":'; exit 0; fi
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-empty" ]; then exit 0; fi
  if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-other" ]; then echo '{"name":"other","status":"Running"}'; exit 0; fi
  for f in "$FAKE_DIR"/inst-*; do
    [ -f "$f" ] || continue
    i=${f##*/inst-}
    loop=true; [ -f "$FAKE_DIR/closed-$i" ] && loop=false
    [ -f "$FAKE_DIR/override-open" ] && loop=true
    vm=vz; [ -f "$FAKE_DIR/override-qemu" ] && vm=qemu
    nest=false; if [ -f "$FAKE_DIR/nested-$i" ] || [ -f "$FAKE_DIR/override-nested" ]; then nest=true; fi
    nest="\"nestedVirtualization\":$nest,"
    if [ -f "$FAKE_DIR/edited" ] && [ -f "$FAKE_DIR/after-edit-list-incomplete" ]; then nest=; fi
    nets=; if [ -f "$FAKE_DIR/networks-json" ]; then nets=",\"networks\":$(cat "$FAKE_DIR/networks-json")"; fi
    printf '{"name":"%s","status":"%s","dir":"%s/%s","vmType":"%s","sshConfigFile":"%s/%s/ssh.config","cpus":2,"config":{%s"networkOpts":{"hostLoopback":%s}%s}}\n' \
      "$i" "$(cat "$f")" "$FAKE_DIR" "$i" "$vm" "$FAKE_DIR" "$i" "$nest" "$loop" "$nets"
  done ;;
create)
  for a; do case $a in --name=*) n=${a#--name=} ;; esac; done
  mkdir -p "$FAKE_DIR/$n"
  for a; do case $a in
    '--set=.networkOpts.hostLoopback = false') touch "$FAKE_DIR/closed-$n" ;;
    --nested-virt) touch "$FAKE_DIR/nested-$n" ;;
  esac; done
  echo Stopped > "$FAKE_DIR/inst-$n" ;;
edit)
  if [ -f "$FAKE_DIR/edit-fails" ]; then echo "fake limactl: edit failed" >&2; exit 1; fi
  prev=
  for a; do
    if [ "$prev" = --set ] && [ "$a" = '.nestedVirtualization = false' ]; then rm -f "$FAKE_DIR/nested-$n"; fi
    prev=$a
  done
  touch "$FAKE_DIR/edited" ;;
start)
  d=$FAKE_DIR/$n
  mkdir -p "$d"
  if [ -f "$FAKE_DIR/refuse-nesting" ] && { [ -f "$FAKE_DIR/nested-$n" ] || [ -f "$FAKE_DIR/override-nested" ]; }; then
    if [ -f "$FAKE_DIR/refuse-late" ]; then
      (sleep 0.3; rm -f "$d/ha.pid"; sleep 0.2; copy "$FAKE_DIR/refuse-ha" > "$d/ha.stderr.log") </dev/null >/dev/null 2>&1 &
      echo $! > "$d/ha.pid"
    elif [ -f "$FAKE_DIR/refuse-ha" ]; then
      copy "$FAKE_DIR/refuse-ha" > "$d/ha.stderr.log"
    fi
    copy "$FAKE_DIR/refuse-stderr" >&2
    exit 1
  fi
  if [ -f "$FAKE_DIR/fail-start" ]; then
    if [ -f "$FAKE_DIR/fail-ha" ]; then copy "$FAKE_DIR/fail-ha" > "$d/ha.stderr.log"; fi
    copy "$FAKE_DIR/fail-start" >&2
    rm -f "$FAKE_DIR/fail-start"
    exit 1
  fi
  echo $$ > "$d/ha.pid"
  echo Running > "$FAKE_DIR/inst-$n" ;;
stop) rm -f "$FAKE_DIR/$n/ha.pid"; echo Stopped > "$FAKE_DIR/inst-$n" ;;
delete)
  if [ -f "$FAKE_DIR/delete-fails" ]; then echo "fake limactl: delete failed" >&2; exit 1; fi
  rm -f "$FAKE_DIR/inst-$n" ;;
*) echo "fake limactl: unknown command $cmd" >&2; exit 2 ;;
esac
`

// fakeLima puts the fake limactl first on PATH, sets the operating system
// that the provider sees, and returns the fake's folder.
func fakeLima(t *testing.T, system string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake limactl is a POSIX shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(fakeLimactl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	t.Setenv("LIMA_HOME", "")
	old := goos
	goos = system
	t.Cleanup(func() { goos = old })
	return dir
}

// set makes the fake's files: a name alone makes an empty file, and
// name=testdata-file copies that file from testdata.
func set(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		name, from, ok := strings.Cut(f, "=")
		var b []byte
		if ok {
			var err error
			if b, err = os.ReadFile(filepath.Join("testdata", from)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// calls returns the fake's calls, each as its arguments.
func calls(t *testing.T, dir string) [][]string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	var out [][]string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			out = append(out, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
		}
	}
	return out
}

// verbs returns the first argument of each call that changes a VM, in
// order: calls of list and --version are left out.
func verbs(cs [][]string) []string {
	var out []string
	for _, c := range cs {
		if c[0] != "list" && c[0] != "--version" {
			out = append(out, c[0])
		}
	}
	return out
}

// called reports whether the fake was called with exactly args.
func called(cs [][]string, args ...string) bool {
	return slices.ContainsFunc(cs, func(c []string) bool { return slices.Equal(c, args) })
}

func TestCreateAndLifecycle(t *testing.T) {
	dir := fakeLima(t, "linux")
	ctx := context.Background()

	var out bytes.Buffer
	h, err := Provider{}.Create(ctx, provider.Request{Name: "dev", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	inst := provider.MachineName("dev")
	if h.SSH != "lima-"+inst || h.SSHConfig != filepath.Join(dir, inst, "ssh.config") || h.Machine.Provider != "lima" || h.Machine.ID != inst || h.Machine.IdleStop != DefaultIdleStop {
		t.Fatalf("host = %+v, machine = %+v", h, h.Machine)
	}
	cs := calls(t, dir)
	if !called(cs, "--version") || !called(cs, CreateArgs(inst, provider.Request{}, "linux")...) || !called(cs, "start", "--tty=false", inst) ||
		!slices.Equal(verbs(cs), []string{"create", "start"}) {
		t.Fatalf("limactl calls: %q", cs)
	}
	// On Linux, Lima runs QEMU, which cannot close the loopback path: the
	// client says so, and makes the VM as before.
	for _, want := range []string{
		"  ! Lima with QEMU cannot stop dev from reaching this machine's loopback services.\n",
		"    Try: run untrusted work on a remote host: " + brand.Name + " host add\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if _, err := (Provider{}).Create(ctx, provider.Request{Name: "dev"}); err == nil {
		t.Fatal("a second instance of the same name was made")
	}

	lc, _ := Provider{}.Open(h)
	status := func() string {
		s, err := lc.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if status() != client.MachineRunning {
		t.Fatal(status())
	}
	tr, err := lc.Transport(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := tr.(*client.SSH)
	if !ok || s.Host.SSH != "lima-"+inst || s.Host.SSHConfig != h.SSHConfig || !s.Interactive {
		t.Fatalf("transport = %#v", tr)
	}
	if err := lc.Stop(ctx); err != nil || status() != client.MachineStopped {
		t.Fatalf("stop: %v, %s", err, status())
	}
	if err := lc.Start(ctx); err != nil || status() != client.MachineRunning {
		t.Fatalf("start: %v, %s", err, status())
	}
	if err := lc.Destroy(ctx); err != nil || status() != client.MachineMissing {
		t.Fatalf("destroy: %v, %s", err, status())
	}
	if err := lc.Destroy(ctx); err != nil {
		t.Fatalf("destroying a missing VM: %v", err)
	}
}

func TestCreateOnAMac(t *testing.T) {
	dir := fakeLima(t, "darwin")
	ctx := context.Background()
	var out bytes.Buffer
	h, err := Provider{}.Create(ctx, provider.Request{Name: "dev", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	inst := provider.MachineName("dev")
	cs := calls(t, dir)
	if !called(cs, CreateArgs(inst, provider.Request{}, "darwin")...) || !slices.Equal(verbs(cs), []string{"create", "start"}) {
		t.Fatalf("limactl calls: %q", cs)
	}
	// The line that the user sees can be pasted into a shell: client.ShellQuote
	// quotes each word that is not only letters, digits and . _ / -.
	if want := "limactl create '--tty=false' '--name=" + inst + "' --plain '--vm-type=vz' '--set=.networkOpts.hostLoopback = false' --nested-virt"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "QEMU") || strings.Contains(out.String(), "microvm") {
		t.Errorf("a Mac that runs nested VMs got a warning:\n%s", out.String())
	}
	if c := LoopbackCheck(ctx, h); !c.OK || c.Name != LoopbackName {
		t.Errorf("the new VM's check = %+v", c)
	}
}

// Lima's override.yaml wins over the arguments of limactl create. A VM that
// it leaves open is deleted before it starts.
func TestCreateRefusesAnOverride(t *testing.T) {
	inst := provider.MachineName("dev")
	for _, tc := range []struct {
		file, setting, limaHome, path string
	}{
		{"override-open", "networkOpts.hostLoopback true", "", "~/.lima/_config/override.yaml"},
		{"override-qemu", "vmType qemu", "/opt/lima", filepath.Join("/opt/lima", "_config", "override.yaml")},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := fakeLima(t, "darwin")
			t.Setenv("LIMA_HOME", tc.limaHome)
			set(t, dir, tc.file)
			_, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev"})
			var ue *client.UserError
			if !errors.As(err, &ue) ||
				ue.Msg != "Lima made "+inst+" with "+tc.setting+", which lets it reach services on your Mac's loopback address, so host create deleted it" ||
				ue.Hint != "remove vmType and networkOpts.hostLoopback from "+tc.path+", then run this again" {
				t.Fatalf("create = %#v", err)
			}
			cs := calls(t, dir)
			if !slices.Equal(verbs(cs), []string{"create", "delete"}) || !called(cs, "delete", "--force", inst) {
				t.Errorf("limactl calls: %q", cs)
			}
			if _, err := os.Stat(filepath.Join(dir, "inst-"+inst)); err == nil {
				t.Error("the open VM was left")
			}
		})
	}
}

// Lima's vz driver refuses hostLoopback false with a user-v2 network, which
// default.yaml or override.yaml can give every VM. Such a VM is deleted
// before it starts, and the Try names the file and the network.
func TestCreateRefusesAUserV2Network(t *testing.T) {
	inst := provider.MachineName("dev")
	for _, tc := range []struct {
		name, networks string
		files          map[string]string // under _config
		file, network  string            // in the Try, "" when the VM starts
	}{
		{"user-v2 from default.yaml", `[{"lima":"user-v2"}]`,
			map[string]string{"default.yaml": "networks:\n- lima: user-v2\n"}, "default.yaml", "user-v2"},
		{"a network of mode user-v2 from override.yaml", `[{"lima":"mine"}]`,
			map[string]string{"networks.yaml": "networks:\n  mine:\n    mode: user-v2\n", "override.yaml": "networks:\n  - lima: mine\n"}, "override.yaml", "mine"},
		{"user-v2 from neither file", `[{"lima":"user-v2"}]`, nil, "", "user-v2"},
		// No network has mode user-v2 and a gateway, so Lima puts its own
		// user-v2 network in place of this entry (fillDefaults).
		{"user-v2 that networks.yaml calls shared", `[{"lima":"user-v2"}]`,
			map[string]string{"networks.yaml": "networks:\n  user-v2:\n    mode: shared\n    gateway: 192.0.2.1\n", "default.yaml": "networks:\n- lima: user-v2\n"}, "default.yaml", "user-v2"},
		{"a shared network starts", `[{"lima":"shared"}]`,
			map[string]string{"networks.yaml": "networks:\n  shared:\n    mode: shared\n", "default.yaml": "networks:\n- lima: shared\n"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeLima(t, "darwin")
			if err := os.WriteFile(filepath.Join(dir, "networks-json"), []byte(tc.networks), 0o644); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(dir, "_config")
			if err := os.MkdirAll(config, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, text := range tc.files {
				if err := os.WriteFile(filepath.Join(config, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev"})
			cs := calls(t, dir)
			if tc.network == "" {
				if err != nil || !slices.Equal(verbs(cs), []string{"create", "start"}) {
					t.Fatalf("create = %v, calls %q", err, cs)
				}
				return
			}
			where := filepath.Join(config, "default.yaml") + " or " + filepath.Join(config, "override.yaml")
			if tc.file != "" {
				where = filepath.Join(config, tc.file)
			}
			var ue *client.UserError
			if !errors.As(err, &ue) ||
				ue.Msg != "Lima gives "+inst+" the user-v2 network "+tc.network+", which cannot go with networkOpts.hostLoopback false, so host create deleted it" ||
				ue.Hint != "remove the network {lima: "+tc.network+"} from networks in "+where+", then run this again" {
				t.Fatalf("create = %#v", err)
			}
			if !slices.Equal(verbs(cs), []string{"create", "delete"}) || !called(cs, "delete", "--force", inst) {
				t.Errorf("limactl calls: %q", cs)
			}
		})
	}
}

// The files in testdata follow Lima 2.3.0's source: each says where from.
// Lima's host agent logs the refusal to ha.stderr.log after the event that
// ends limactl start. limactl start passes it on only in a rare order.
func TestCreateStartsAgainWithoutNesting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"no device, in ha.stderr.log", []string{"refuse-stderr=start-exiting.stderr", "refuse-ha=ha-no-device.log"}},
		{"macOS 14, in ha.stderr.log", []string{"refuse-stderr=start-exiting.stderr", "refuse-ha=ha-old-macos.log"}},
		{"no device, in limactl's stderr", []string{"refuse-stderr=start-no-device.stderr"}},
		{"macOS 14, logged after limactl start returns", []string{"refuse-stderr=start-exiting.stderr", "refuse-ha=ha-old-macos.log", "refuse-late"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeLima(t, "darwin")
			set(t, dir, append(tc.files, "refuse-nesting")...)
			var out bytes.Buffer
			h, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev", Out: &out})
			if err != nil {
				t.Fatalf("create: %v\n%s", err, out.String())
			}
			inst := provider.MachineName("dev")
			cs := calls(t, dir)
			if !slices.Equal(verbs(cs), []string{"create", "start", "edit", "start"}) ||
				!called(cs, "edit", "--tty=false", "--set", ".nestedVirtualization = false", inst) {
				t.Fatalf("limactl calls: %q", cs)
			}
			if want := "  This Mac cannot run a VM inside a VM, so dev gets no microvm sandboxes.\n"; !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
			if h.Machine == nil || h.Machine.ID != inst {
				t.Errorf("host = %+v", h)
			}
		})
	}
}

func TestCreateReportsOtherStartFailures(t *testing.T) {
	dir := fakeLima(t, "darwin")
	set(t, dir, "fail-start=start-exiting.stderr", "fail-ha=ha-other.log")
	var out bytes.Buffer
	_, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev", Out: &out})
	var ee *provider.ExecError
	if !errors.As(err, &ee) || !strings.HasPrefix(err.Error(), "limactl start: ") || !strings.Contains(ee.Stderr, "exiting, status=") {
		t.Fatalf("create = %v", err)
	}
	if cs := calls(t, dir); !slices.Equal(verbs(cs), []string{"create", "start"}) {
		t.Errorf("limactl calls after another failure: %q", cs)
	}
	if strings.Contains(out.String(), "microvm") {
		t.Errorf("output:\n%s", out.String())
	}
}

// When the start again fails, the VM, which no host names, is deleted.
func TestCreateDeletesAVMThatDoesNotStartAgain(t *testing.T) {
	inst := provider.MachineName("dev")
	again := brand.Name + " host create lima dev   (to try again)"
	refused := []string{"refuse-nesting", "refuse-stderr=start-exiting.stderr", "refuse-ha=ha-no-device.log"}
	for _, tc := range []struct {
		name         string
		files        []string
		verbs        []string
		prefix       string
		suffix, hint string
		kept         bool
	}{
		{"the edit fails", append(refused, "edit-fails"), []string{"create", "start", "edit", "delete"},
			"Lima could not turn nested virtualisation off for " + inst + " (limactl edit: exit status 1: fake limactl: edit failed)",
			", so host create deleted it", again, false},
		// The edit works, but Lima's override.yaml keeps nesting on: no start
		// again, and the Try names the file.
		{"an override keeps nesting on", append(refused, "override-nested"), []string{"create", "start", "edit", "delete"},
			"Lima's override.yaml keeps nested virtualisation on for " + inst + ", which this Mac cannot run",
			", so host create deleted it", "remove nestedVirtualization from ~/.lima/_config/override.yaml, then run this again", false},
		// A read after the edit that fails, is not JSON, or leaves out
		// nesting: no start again.
		{"the read after the edit fails", append(refused, "after-edit-list-fails"), []string{"create", "start", "edit", "delete"},
			"could not read " + inst + " from limactl after the edit (limactl list: exit status 1: fake limactl: list failed)",
			", so host create deleted it", again, false},
		{"the read after the edit is not JSON", append(refused, "after-edit-list-bad"), []string{"create", "start", "edit", "delete"},
			"could not read " + inst + " from limactl after the edit (read limactl list: ",
			", so host create deleted it", again, false},
		{"the read after the edit leaves out nesting", append(refused, "after-edit-list-incomplete"), []string{"create", "start", "edit", "delete"},
			"limactl did not say whether nested virtualisation is off for " + inst,
			", so host create deleted it", again, false},
		// A list that works but leaves the VM out: delete it all the same.
		{"the list after the edit is empty", append(refused, "after-edit-list-empty"), []string{"create", "start", "edit", "delete"},
			"limactl did not list " + inst + " after the edit", ", so host create deleted it", again, false},
		{"the list after the edit holds another VM", append(refused, "after-edit-list-other"), []string{"create", "start", "edit", "delete"},
			"limactl did not list " + inst + " after the edit", ", so host create deleted it", again, false},
		{"the second start fails", append(refused, "fail-start=start-exiting.stderr", "fail-ha=ha-other.log"), []string{"create", "start", "edit", "start", "delete"},
			inst + " did not start without nested virtualisation either (limactl start: exit status 1: ",
			", so host create deleted it", again, false},
		{"the delete fails too", append(refused, "edit-fails", "delete-fails"), []string{"create", "start", "edit", "delete"},
			"Lima could not turn nested virtualisation off for " + inst + " (limactl edit: exit status 1: fake limactl: edit failed)",
			", and host create could not delete it: limactl delete: exit status 1: fake limactl: delete failed",
			"limactl delete --force " + inst + ", then " + again, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeLima(t, "darwin")
			set(t, dir, tc.files...)
			_, err := Provider{}.Create(context.Background(), provider.Request{Name: "dev"})
			var ue *client.UserError
			if !errors.As(err, &ue) || !strings.HasPrefix(ue.Msg, tc.prefix) || !strings.HasSuffix(ue.Msg, tc.suffix) || ue.Hint != tc.hint {
				t.Fatalf("create = %#v", err)
			}
			cs := calls(t, dir)
			if !slices.Equal(verbs(cs), tc.verbs) || !called(cs, "delete", "--force", inst) {
				t.Errorf("limactl calls: %q", cs)
			}
			_, serr := os.Stat(filepath.Join(dir, "inst-"+inst))
			if (serr == nil) != tc.kept {
				t.Errorf("VM kept = %v, want %v", serr == nil, tc.kept)
			}
		})
	}
}
