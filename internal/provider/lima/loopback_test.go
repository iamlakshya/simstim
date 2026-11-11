package lima

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// readData reads a file of testdata without its notes: the lines that start
// with #.
func readData(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(b, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("#")) {
			out.Write(line)
		}
	}
	return out.Bytes()
}

// testdata/loopback.ndjson is `limactl list --json` written from Lima
// 2.3.0's source, as its first line says: hostLoopback false, true and
// absent, and a VM on QEMU. testdata/list.ndjson is a recording from Lima
// 2.2.0, cut down to leave out the config: a VM made before host create
// closed the path.
func TestLoopbackCheck(t *testing.T) {
	old := goos
	t.Cleanup(func() { goos = old })
	goos = "linux"
	b := readData(t, "loopback.ndjson")
	recorded := readData(t, "list.ndjson")
	open := func(inst string) string {
		return "limactl stop " + inst + ", then limactl edit --set '.networkOpts.hostLoopback = false' " + inst + ", then limactl start " + inst
	}
	for _, tc := range []struct {
		list        []byte
		inst        string
		ok          bool
		detail, fix string
	}{
		{b, "closed", true, "box cannot reach services on your Mac's loopback address", ""},
		{b, "open", false, "box can reach services on your Mac's loopback address", open("open")},
		{b, "absent", false, "box can reach services on your Mac's loopback address", open("absent")},
		{b, "qemu", false, "Lima with QEMU cannot stop box from reaching this machine's loopback services", "run untrusted work on a remote host: " + brand.Name + " host add"},
		{recorded, provider.MachineName("ltest"), false, "box can reach services on your Mac's loopback address", open(provider.MachineName("ltest"))},
		{b, "nope", false, "Lima has no instance called nope", "limactl list"},
	} {
		in, err := parseList(tc.list, tc.inst)
		if err != nil {
			t.Fatal(err)
		}
		c := loopbackCheck("box", tc.inst, in)
		if c.Name != LoopbackName || c.OK != tc.ok || c.Detail != tc.detail || c.Fix != tc.fix {
			t.Errorf("%s: check = %+v", tc.inst, c)
		}
	}
	if c := loopbackCheck("box", "x", &instance{VMType: "krunkit"}); c.OK || !strings.HasPrefix(c.Detail, "Lima with krunkit cannot stop box") {
		t.Errorf("another VM type: %+v", c)
	}
	if c := loopbackCheck("box", "x", &instance{}); c.OK || !strings.HasPrefix(c.Detail, "Lima with an unknown VM type cannot stop box") {
		t.Errorf("empty VM type: %+v", c)
	}
	// On a Mac, a new VM uses vz, so the way out is a new host.
	goos = "darwin"
	in, _ := parseList(b, "qemu")
	if c := loopbackCheck("box", "qemu", in); c.OK || c.Fix != brand.Name+" host destroy box, then "+brand.Name+" host create lima box" {
		t.Errorf("a QEMU VM on a Mac: %+v", c)
	}
}

func TestLoopbackCheckReadsLimactl(t *testing.T) {
	ctx := context.Background()
	h := client.Host{Name: "dev", Machine: &client.Machine{Provider: "lima", ID: provider.MachineName("dev")}}
	path := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	if c := LoopbackCheck(ctx, h); c.OK || !strings.HasPrefix(c.Detail, "could not read the VM from limactl: limactl is not installed") || c.Fix != "limactl list --json" {
		t.Errorf("without limactl: %+v", c)
	}
	t.Setenv("PATH", path)
	dir := fakeLima(t, "darwin")
	if c := LoopbackCheck(ctx, h); c.OK || c.Detail != "Lima has no instance called "+h.Machine.ID {
		t.Errorf("without the VM: %+v", c)
	}
	// A VM that an older client made keeps Lima's default: open.
	v := fakeVM(t, dir, h.Machine.ID)
	v.status("Running")
	v.file(filepath.Join(h.Machine.ID, "ha.pid"), time.Now())
	if c := LoopbackCheck(ctx, h); c.OK || c.Detail != "dev can reach services on your Mac's loopback address" || !strings.HasPrefix(c.Fix, "limactl stop "+h.Machine.ID+", then limactl edit") {
		t.Errorf("an open VM: %+v", c)
	}
	v.set("closed-" + h.Machine.ID)
	if c := LoopbackCheck(ctx, h); !c.OK || c.Detail != "dev cannot reach services on your Mac's loopback address" {
		t.Errorf("a closed VM: %+v", c)
	}
	if c := LoopbackCheck(ctx, client.Host{Name: "plain"}); c.OK || c.Detail != "plain was not made by host create lima" {
		t.Errorf("a host without a machine: %+v", c)
	}
}

// A running VM keeps the network that it started with. When its settings
// changed after its host agent started, the check passes only after a
// restart, in either direction.
func TestLoopbackCheckAfterAChange(t *testing.T) {
	ctx := context.Background()
	h := client.Host{Name: "dev", Machine: &client.Machine{Provider: "lima", ID: provider.MachineName("dev")}}
	inst := h.Machine.ID
	restart := "limactl stop " + inst + ", then limactl start " + inst
	const changed = "the Lima settings of dev changed after its VM started, so they apply only after a restart"
	const opened = changed + ", and they let dev reach services on your Mac's loopback address"
	edit := "limactl stop " + inst + ", then limactl edit --set '.networkOpts.hostLoopback = false' " + inst + ", then limactl start " + inst
	for _, tc := range []struct {
		name, status string
		closed       bool
		newer        []string // files, of the VM's folder or of _config, made after the start
		noPID        bool
		ok           bool
		detail, fix  string
	}{
		{"closed, nothing changed", "Running", true, nil, false, true, "dev cannot reach services on your Mac's loopback address", ""},
		{"closed after the start (it was open)", "Running", true, []string{"lima.yaml"}, false, false, changed, restart},
		// The other direction: the running VM is still closed, and the
		// warning says that the open setting waits for a restart.
		{"opened after the start (it was closed)", "Running", false, []string{"lima.yaml"}, false, false, opened, edit},
		{"open, nothing changed", "Running", false, nil, false, false, "dev can reach services on your Mac's loopback address", edit},
		{"override.yaml changed", "Running", true, []string{"_config/override.yaml"}, false, false, changed, restart},
		{"default.yaml changed", "Running", true, []string{"_config/default.yaml"}, false, false, changed, restart},
		{"a settings file was removed", "Running", true, []string{"_config"}, false, false, changed, restart},
		{"no ha.pid, an older ha.sock", "Running", true, nil, true, true, "dev cannot reach services on your Mac's loopback address", ""},
		{"stopped: the next start reads the change", "Stopped", true, []string{"lima.yaml"}, false, true, "dev cannot reach services on your Mac's loopback address", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeLima(t, "darwin")
			v := fakeVM(t, dir, inst)
			v.status(tc.status)
			if tc.closed {
				v.set("closed-" + inst)
			}
			start := time.Now().Add(-time.Hour)
			v.file(filepath.Join(inst, "lima.yaml"), start.Add(-time.Hour))
			v.file("_config/override.yaml", start.Add(-time.Hour))
			v.file("_config/default.yaml", start.Add(-time.Hour))
			v.file("_config", start.Add(-time.Hour))
			if tc.noPID {
				v.file(filepath.Join(inst, "ha.sock"), start)
			} else {
				v.file(filepath.Join(inst, "ha.pid"), start)
			}
			for _, f := range tc.newer {
				if f != "lima.yaml" {
					v.file(f, start.Add(time.Minute))
				} else {
					v.file(filepath.Join(inst, f), start.Add(time.Minute))
				}
			}
			c := LoopbackCheck(ctx, h)
			if c.OK != tc.ok || c.Detail != tc.detail || (tc.fix != "" && c.Fix != tc.fix) {
				t.Errorf("check = %+v", c)
			}
		})
	}
	// A running VM with neither ha.pid nor ha.sock: its start is not known.
	dir := fakeLima(t, "darwin")
	v := fakeVM(t, dir, inst)
	v.status("Running")
	v.set("closed-" + inst)
	if c := LoopbackCheck(ctx, h); c.OK || c.Detail != changed || c.Fix != restart {
		t.Errorf("an unknown start: %+v", c)
	}
	// Opened after the start by override.yaml: the warning names the
	// restart, and the Try keeps the override's remedy.
	if err := os.WriteFile(filepath.Join(dir, inst, "ha.pid"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	v.set("override-open")
	v.write(filepath.Join(inst, "lima.yaml"), "networkOpts:\n  hostLoopback: false\n")
	v.file("_config/override.yaml", time.Now().Add(time.Hour))
	if c := LoopbackCheck(ctx, h); c.OK || c.Detail != opened ||
		c.Fix != "remove networkOpts.hostLoopback from ~/.lima/_config/override.yaml, then limactl stop "+inst+", then limactl start "+inst {
		t.Errorf("opened by override.yaml after the start: %+v", c)
	}
}

// When the VM's own lima.yaml closes the path but Lima's override.yaml
// opens it, the edit of the three commands does nothing: the Try names the
// file. Once the file no longer opens it, the check passes.
func TestLoopbackCheckNamesAnOverride(t *testing.T) {
	ctx := context.Background()
	h := client.Host{Name: "dev", Machine: &client.Machine{Provider: "lima", ID: provider.MachineName("dev")}}
	inst := h.Machine.ID
	dir := fakeLima(t, "darwin")
	v := fakeVM(t, dir, inst)
	v.status("Stopped")
	if c := LoopbackCheck(ctx, h); c.OK || c.Fix != "limactl stop "+inst+", then limactl edit --set '.networkOpts.hostLoopback = false' "+inst+", then limactl start "+inst {
		t.Errorf("an open VM without an override: %+v", c)
	}
	v.set("closed-"+inst, "override-open")
	named := "remove networkOpts.hostLoopback from ~/.lima/_config/override.yaml, then limactl stop " + inst + ", then limactl start " + inst
	// The forms of YAML that the old line scan missed, and the block that
	// limactl create writes.
	for _, own := range []string{
		"vmType: vz\nnetworkOpts:\n  # set by host create\n  hostLoopback: false\n",
		`networkOpts: {"hostLoopback": false}` + "\n",
		"networkOpts:\n  \"hostLoopback\": False\n",
	} {
		v.write(filepath.Join(inst, "lima.yaml"), own)
		if c := LoopbackCheck(ctx, h); c.OK || c.Fix != named {
			t.Errorf("an open VM with an override, own lima.yaml %q: %+v", own, c)
		}
	}
	if err := os.Remove(filepath.Join(dir, "override-open")); err != nil {
		t.Fatal(err)
	}
	if c := LoopbackCheck(ctx, h); !c.OK {
		t.Errorf("after the override went: %+v", c)
	}
}

// Lima's vz driver refuses hostLoopback false with a user-v2 network. For
// an open VM with one, the Try removes the network first, and names the
// file that lists it, so that the VM can start again.
func TestLoopbackCheckNamesAUserV2Network(t *testing.T) {
	ctx := context.Background()
	h := client.Host{Name: "dev", Machine: &client.Machine{Provider: "lima", ID: provider.MachineName("dev")}}
	inst := h.Machine.ID
	dir := fakeLima(t, "darwin")
	v := fakeVM(t, dir, inst)
	v.status("Stopped")
	v.write("networks-json", `[{"lima":"user-v2"}]`)
	edit := "limactl stop " + inst + ", then limactl edit --set '.networkOpts.hostLoopback = false' " + inst + ", then limactl start " + inst
	v.write("_config/default.yaml", "networks:\n- lima: user-v2\n")
	if c := LoopbackCheck(ctx, h); c.OK || c.Fix != "remove the network {lima: user-v2} from networks in "+filepath.Join(dir, "_config", "default.yaml")+", then "+edit {
		t.Errorf("a user-v2 network from default.yaml: %+v", c)
	}
	// The network in the VM's own lima.yaml, which closes the path while
	// override.yaml opens it: both removals come before the restart.
	own := filepath.Join(dir, inst, "lima.yaml")
	v.write("_config/default.yaml", "")
	v.write(filepath.Join(inst, "lima.yaml"), "networks:\n- lima: user-v2\nnetworkOpts:\n  hostLoopback: false\n")
	v.set("closed-"+inst, "override-open")
	if c := LoopbackCheck(ctx, h); c.OK || c.Fix != "remove the network {lima: user-v2} from networks in "+own+
		", then remove networkOpts.hostLoopback from ~/.lima/_config/override.yaml, then limactl stop "+inst+", then limactl start "+inst {
		t.Errorf("a user-v2 network from the VM's lima.yaml: %+v", c)
	}
}

func TestOwnLoopbackClosed(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want bool
	}{
		{"networkOpts:\n  hostLoopback: false\n", true},
		{"# a note\nvmType: vz\nnetworkOpts:\n  # a note\n  hostLoopback: false # set by host create\nmounts: []\n", true},
		{"networkOpts: {hostLoopback: false}\n", true},
		{"networkOpts:\n  hostLoopback: true\n", false},
		{"networkOpts:\n  hostLoopback: null\n", false},
		{"other:\n  hostLoopback: false\nnetworkOpts:\n  hostLoopback: true\n", false},
		{"# networkOpts:\n#   hostLoopback: false\n", false},
		{"", false},
		// The YAML grammar, not a line scan: quoted keys, the JSON form,
		// and the capitalised boolean of YAML 1.2.
		{"networkOpts:\n  \"hostLoopback\": false\n", true},
		{"\"networkOpts\":\n  'hostLoopback': false\n", true},
		{`networkOpts: {"hostLoopback": false}` + "\n", true},
		{`{"networkOpts": {"hostLoopback": false}}`, true},
		{"networkOpts:\n  hostLoopback: False\n", true},
		{"networkOpts:\n  hostLoopback: FALSE\n", true},
		{"networkOpts:\n  hostLoopback: \"false\"\n", false},
		{"networkOpts: [hostLoopback]\n", false},
		{"networkOpts:\n  hostLoopback: false\n bad indent: [\n", false},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ownLoopbackClosed(dir); got != tc.want {
			t.Errorf("ownLoopbackClosed(%q) = %v, want %v", tc.yaml, got, tc.want)
		}
	}
	if ownLoopbackClosed("") || ownLoopbackClosed(t.TempDir()) {
		t.Error("a VM without a lima.yaml counts as closed")
	}
}

// vmFiles makes the files of one VM for the fake limactl.
type vmFiles struct {
	t        *testing.T
	dir      string // the fake's folder, which stands in for $LIMA_HOME
	instance string
}

func fakeVM(t *testing.T, dir, inst string) vmFiles {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, inst), 0o755); err != nil {
		t.Fatal(err)
	}
	return vmFiles{t: t, dir: dir, instance: inst}
}

func (v vmFiles) status(s string) { v.write("inst-"+v.instance, s+"\n") }

func (v vmFiles) set(files ...string) { set(v.t, v.dir, files...) }

func (v vmFiles) write(name, text string) {
	v.t.Helper()
	p := filepath.Join(v.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		v.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		v.t.Fatal(err)
	}
}

// file makes the file or folder name, under the fake's folder, with the
// time at.
func (v vmFiles) file(name string, at time.Time) {
	v.t.Helper()
	p := filepath.Join(v.dir, name)
	if _, err := os.Stat(p); err != nil {
		if name != "_config" {
			v.write(name, "")
		} else if err := os.MkdirAll(p, 0o755); err != nil {
			v.t.Fatal(err)
		}
	}
	if err := os.Chtimes(p, at, at); err != nil {
		v.t.Fatal(err)
	}
}
