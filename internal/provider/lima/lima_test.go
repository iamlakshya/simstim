package lima

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	got := strings.Join(CreateArgs("vm", provider.Request{CPUs: 4, Memory: 8 << 30, Disk: 80 << 30}, "darwin"), " ")
	if !strings.Contains(got, "--cpus=4 --memory=8 --disk=80") {
		t.Fatal(got)
	}
	got = strings.Join(CreateArgs("vm", provider.Request{Memory: 1536 << 20, Disk: 512 << 20}, "darwin"), " ")
	if !strings.Contains(got, "--memory=1.5 --disk=0.5") {
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

func TestCreateAndLifecycle(t *testing.T) {
	dir := fakeLima(t, "linux")
	ctx := context.Background()

	var out bytes.Buffer
	h, err := fixtureCreate(t, ctx, provider.Request{Name: "dev", Out: &out})
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
	if _, err := fixtureCreate(t, ctx, provider.Request{Name: "dev"}); err == nil {
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
	h, err := fixtureCreate(t, ctx, provider.Request{Name: "dev", Out: &out})
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
// it leaves open is retained without starting it.
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
			if tc.limaHome == "" {
				home, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("HOME", home)
				t.Setenv("LIMA_HOME", "")
			} else {
				t.Setenv("LIMA_HOME", filepath.Join(dir, "explicit-home"))
			}
			set(t, dir, tc.file)
			_, err := fixtureCreate(t, context.Background(), provider.Request{Name: "dev"})
			var ue *client.UserError
			if !errors.As(retainedCreationCause(err), &ue) ||
				ue.Msg != "Lima made "+inst+" with "+tc.setting+", which lets it reach services on your Mac's loopback address; the VM is retained for inspection" ||
				ue.Hint != retainedHintPrefix+"remove vmType and networkOpts.hostLoopback from "+overridePath()+", then run this again" {
				t.Fatalf("create = %#v", err)
			}
			cs := calls(t, dir)
			if !slices.Equal(verbs(cs), []string{"create"}) || called(cs, "delete", "--force", inst) {
				t.Errorf("limactl calls: %q", cs)
			}
			if _, err := os.Stat(filepath.Join(dir, "inst-"+inst)); err != nil {
				t.Error("the actual partial VM was lost")
			}
		})
	}
}

// Lima's vz driver refuses hostLoopback false with a user-v2 network, which
// default.yaml or override.yaml can give every VM. Such a VM is retained
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
			_, err := fixtureCreate(t, context.Background(), provider.Request{Name: "dev"})
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
			if !errors.As(retainedCreationCause(err), &ue) ||
				ue.Msg != "Lima gives "+inst+" the user-v2 network "+tc.network+", which cannot go with networkOpts.hostLoopback false; the VM is retained for inspection" ||
				ue.Hint != retainedHintPrefix+"remove the network {lima: "+tc.network+"} from networks in "+where+", then run this again" {
				t.Fatalf("create = %#v", err)
			}
			if !slices.Equal(verbs(cs), []string{"create"}) || called(cs, "delete", "--force", inst) {
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
			h, err := fixtureCreate(t, context.Background(), provider.Request{Name: "dev", Out: &out})
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
	_, err := fixtureCreate(t, context.Background(), provider.Request{Name: "dev", Out: &out})
	var ee *provider.ExecError
	if !errors.As(err, &ee) || !strings.HasPrefix(retainedCreationCause(err).Error(), "limactl start: ") || !strings.Contains(ee.Stderr, "exiting, status=") {
		t.Fatalf("create = %v", err)
	}
	if cs := calls(t, dir); !slices.Equal(verbs(cs), []string{"create", "start"}) {
		t.Errorf("limactl calls after another failure: %q", cs)
	}
	if strings.Contains(out.String(), "microvm") {
		t.Errorf("output:\n%s", out.String())
	}
}

// When the start again fails, the actual VM is retained for inspection.
func TestCreateRetainsAVMThatDoesNotStartAgain(t *testing.T) {
	inst := provider.MachineName("dev")
	again := brand.Name + " host create lima dev (to try again)"
	refused := []string{"refuse-nesting", "refuse-stderr=start-exiting.stderr", "refuse-ha=ha-no-device.log"}
	for _, tc := range []struct {
		name         string
		files        []string
		verbs        []string
		prefix       string
		suffix, hint string
		kept         bool
	}{
		{"the edit fails", append(refused, "edit-fails"), []string{"create", "start", "edit"},
			"Lima could not turn nested virtualisation off for " + inst + " (limactl edit: exit status 1: fake limactl: edit failed)",
			"; the VM is retained for inspection", again, true},
		// The edit works, but Lima's override.yaml keeps nesting on: no start
		// again, and the Try names the file.
		{"an override keeps nesting on", append(refused, "override-nested"), []string{"create", "start", "edit"},
			"Lima's override.yaml keeps nested virtualisation on for " + inst + ", which this Mac cannot run",
			"; the VM is retained for inspection", "remove nestedVirtualization from ~/.lima/_config/override.yaml, then run this again", true},
		// A read after the edit that fails, is not JSON, or leaves out
		// nesting: no start again.
		{"the read after the edit fails", append(refused, "after-edit-list-fails"), []string{"create", "start", "edit"},
			"could not read " + inst + " from limactl after the edit (limactl list: exit status 1: fake limactl: list failed)",
			"; the VM is retained for inspection", again, true},
		{"the read after the edit is not JSON", append(refused, "after-edit-list-bad"), []string{"create", "start", "edit"},
			"could not read " + inst + " from limactl after the edit (read limactl list: ",
			"; the VM is retained for inspection", again, true},
		{"the read after the edit leaves out nesting", append(refused, "after-edit-list-incomplete"), []string{"create", "start", "edit"},
			"limactl did not say whether nested virtualisation is off for " + inst,
			"; the VM is retained for inspection", again, true},
		// An incomplete list cannot authorise deleting a reusable instance name.
		{"the list after the edit is empty", append(refused, "after-edit-list-empty"), []string{"create", "start", "edit"},
			"limactl did not list " + inst + " after the edit", "; the VM is retained for inspection", again, true},
		{"the list after the edit holds another VM", append(refused, "after-edit-list-other"), []string{"create", "start", "edit"},
			"limactl did not list " + inst + " after the edit", "; the VM is retained for inspection", again, true},
		{"the second start fails", append(refused, "fail-start=start-exiting.stderr", "fail-ha=ha-other.log"), []string{"create", "start", "edit", "start"},
			inst + " did not start without nested virtualisation either (limactl start: exit status 1: ",
			"; the VM is retained for inspection", again, true},
		{"a failing delete recipe is never attempted", append(refused, "edit-fails", "delete-fails"), []string{"create", "start", "edit"},
			"Lima could not turn nested virtualisation off for " + inst + " (limactl edit: exit status 1: fake limactl: edit failed)",
			"; the VM is retained for inspection",
			again, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeLima(t, "darwin")
			set(t, dir, tc.files...)
			_, err := fixtureCreate(t, context.Background(), provider.Request{Name: "dev"})
			var ue *client.UserError
			if !errors.As(retainedCreationCause(err), &ue) || !strings.HasPrefix(ue.Msg, tc.prefix) || !strings.HasSuffix(ue.Msg, tc.suffix) || ue.Hint != retainedHintPrefix+strings.ReplaceAll(tc.hint, "~/.lima", dir) {
				t.Fatalf("create = %#v", err)
			}
			cs := calls(t, dir)
			if !slices.Equal(verbs(cs), tc.verbs) || called(cs, "delete", "--force", inst) {
				t.Errorf("limactl calls: %q", cs)
			}
			_, serr := os.Stat(filepath.Join(dir, "inst-"+inst))
			if (serr == nil) != tc.kept {
				t.Errorf("VM kept = %v, want %v", serr == nil, tc.kept)
			}
		})
	}
}

// Fractional byte requests retain the platform's isolation arguments.
func TestByteSizesKeepLimaPlatformGuards(t *testing.T) {
	for _, system := range []string{"darwin", "linux"} {
		args := CreateArgs("vm", provider.Request{Memory: 1536 << 20, Disk: 512 << 20}, system)
		for _, want := range []string{"--plain", "--memory=1.5", "--disk=0.5"} {
			if !slices.Contains(args, want) {
				t.Errorf("%s byte sizes lack %q: %q", system, want, args)
			}
		}
		for _, guard := range []string{"--vm-type=vz", "--set=.networkOpts.hostLoopback = false", "--nested-virt"} {
			if slices.Contains(args, guard) != (system == "darwin") {
				t.Errorf("%s byte sizes have incorrect %q: %q", system, guard, args)
			}
		}
	}
}
