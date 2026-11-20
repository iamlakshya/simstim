// Package lima makes hosts that are local Lima VMs: Ubuntu LTS with systemd
// and sshd, reached with the system ssh through Lima's own generated SSH
// config. It shells out to limactl; Lima has no API.
package lima

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

const install = "Install Lima (https://lima-vm.io/docs/installation/), then run this again."

// Defaults for a new VM: small, and a sparse disk.
const (
	DefaultCPUs      = 2
	DefaultMemoryGiB = 4
	DefaultDiskGiB   = 40
	// Template is Lima's Ubuntu LTS template.
	Template = "template:ubuntu-lts"
	// DefaultIdleStop is how long a VM idles before it powers itself off.
	DefaultIdleStop = "30m"
)

// goos is the operating system of this computer. Tests change it.
var goos = runtime.GOOS

// Provider makes Lima VMs.
type Provider struct{}

// Name is "lima".
func (Provider) Name() string { return "lima" }

// instance is one line of `limactl list --json`.
type instance struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	Dir           string `json:"dir"`
	VMType        string `json:"vmType"`
	SSHConfigFile string `json:"sshConfigFile"`
	SSHLocalPort  int    `json:"sshLocalPort"`
	CPUs          int    `json:"cpus"`
	Memory        int64  `json:"memory"`
	Disk          int64  `json:"disk"`
	Config        *struct {
		NestedVirtualization *bool `json:"nestedVirtualization"`
		NetworkOpts          struct {
			HostLoopback *bool `json:"hostLoopback"`
		} `json:"networkOpts"`
		Networks []struct {
			Lima string `json:"lima"`
		} `json:"networks"`
	} `json:"config"`
}

// find returns the instance called name, or nil when there is none.
func find(ctx context.Context, name string) (*instance, error) {
	out, err := provider.Exec(ctx, install, "limactl", "list", "--json")
	if err != nil {
		return nil, err
	}
	return parseList(out, name)
}

// parseList reads the NDJSON of `limactl list --json`: one object per line.
func parseList(out []byte, name string) (*instance, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var in instance
		if err := json.Unmarshal(line, &in); err != nil {
			return nil, fmt.Errorf("read limactl list: %w", err)
		}
		if in.Name == name {
			return &in, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read limactl's list: %w", err)
	}
	return nil, nil
}

// CreateArgs are the limactl arguments that make the VM on the operating
// system named system, as runtime.GOOS names it. --plain turns off the
// home-folder mount and the forwarding of every guest port to this machine.
// On macOS, --vm-type=vz wins over any vmType in the user's default.yaml so
// that hostLoopback can close the loopback path, and --nested-virt asks Lima
// to run Apple's own nesting check when the VM starts.
func CreateArgs(inst string, req provider.Request, system string) []string {
	cpus, mem, disk := req.CPUs, req.MemoryGiB, req.DiskGiB
	if cpus <= 0 {
		cpus = DefaultCPUs
	}
	if mem <= 0 {
		mem = DefaultMemoryGiB
	}
	if disk <= 0 {
		disk = DefaultDiskGiB
	}
	args := []string{"create", "--tty=false", "--name=" + inst, "--plain"}
	if system == "darwin" {
		args = append(args, "--vm-type=vz", "--set=.networkOpts.hostLoopback = false", "--nested-virt")
	}
	return append(args, fmt.Sprintf("--cpus=%d", cpus), fmt.Sprintf("--memory=%d", mem), fmt.Sprintf("--disk=%d", disk), Template)
}

// Create makes and starts the VM, and returns the host that reaches it
// through Lima's ssh.config.
func (Provider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	out := req.Out
	if out == nil {
		out = io.Discard
	}
	if err := checkVersion(ctx); err != nil {
		return client.Host{}, err
	}
	inst := provider.MachineName(req.Name)
	if in, err := find(ctx, inst); err != nil {
		return client.Host{}, err
	} else if in != nil {
		return client.Host{}, &client.UserError{Msg: "Lima already has an instance called " + inst, Hint: "Pick another name, or delete the instance with limactl delete " + inst + ".", Code: "instance_exists"}
	}
	if goos != "darwin" {
		fmt.Fprintf(out, "  ! Lima with QEMU cannot stop %s from reaching this machine's loopback services.\n", req.Name)
		fmt.Fprintf(out, "    Try: run untrusted work on a remote host: %s host add\n", brand.Name)
	}
	args := CreateArgs(inst, req, goos)
	fmt.Fprintf(out, "  Creating the VM: limactl %s\n", shellLine(args))
	if _, err := provider.Exec(ctx, install, "limactl", args...); err != nil {
		return client.Host{}, err
	}
	if goos == "darwin" {
		if err := checkClosed(ctx, inst); err != nil {
			return client.Host{}, err
		}
	}
	if err := startNew(ctx, out, req.Name, inst); err != nil {
		return client.Host{}, err
	}
	in, err := find(ctx, inst)
	if err != nil {
		return client.Host{}, err
	}
	if in == nil || in.SSHConfigFile == "" {
		return client.Host{}, fmt.Errorf("limactl did not report the SSH config of %s", inst)
	}
	idle := req.IdleStop
	if idle == "" {
		idle = DefaultIdleStop
	}
	return client.Host{
		Name: req.Name, SSH: sshAlias(inst), SSHConfig: in.SSHConfigFile,
		Machine: &client.Machine{Provider: "lima", ID: inst, IdleStop: idle},
	}, nil
}

// shellLine is args as they would be typed in a shell, for the progress
// lines.
func shellLine(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = client.ShellQuote(a)
	}
	return strings.Join(q, " ")
}

// discard deletes a VM that Create made but cannot hand over, so that no
// VM is left that no host names. The error says what went wrong, and hint
// what to try next.
func discard(ctx context.Context, inst, problem, hint string) error {
	if _, err := provider.Exec(ctx, install, "limactl", "delete", "--force", inst); err != nil {
		return &client.UserError{
			Msg:  fmt.Sprintf("%s, and host create could not delete it: %v", problem, err),
			Hint: "limactl delete --force " + inst + ", then " + hint,
			Code: "lima_vm",
		}
	}
	return &client.UserError{Msg: problem + ", so host create deleted it", Hint: hint, Code: "lima_vm"}
}

// sshAlias is the Host that Lima's ssh.config defines for an instance.
func sshAlias(inst string) string { return "lima-" + inst }

// Open returns the lifecycle of a VM this provider made.
func (Provider) Open(h client.Host) (client.Lifecycle, error) {
	return &vm{h: h, inst: h.Machine.ID}, nil
}

type vm struct {
	h    client.Host
	inst string
	// last is what Status last read while the VM was running, so that
	// Transport need not ask again.
	last *instance
}

// Status maps Lima's status to the client's machine states.
func (v *vm) Status(ctx context.Context) (string, error) {
	in, err := find(ctx, v.inst)
	if err != nil {
		return "", err
	}
	if in == nil {
		return client.MachineMissing, nil
	}
	st := state(in.Status)
	if st == client.MachineRunning {
		v.last = in
	}
	return st, nil
}

func state(s string) string {
	switch s {
	case "Running":
		return client.MachineRunning
	case "Installing", "":
		return client.MachineStarting
	default: // Stopped, Broken, Uninitialized: start tries again
		return client.MachineStopped
	}
}

// Start boots the VM and returns when Lima says SSH works.
func (v *vm) Start(ctx context.Context) error {
	return startVM(ctx, v.inst)
}

// Stop shuts the VM down. Its disk stays.
func (v *vm) Stop(ctx context.Context) error {
	_, err := provider.Exec(ctx, install, "limactl", "stop", v.inst)
	return err
}

// Destroy deletes the VM and its disk.
func (v *vm) Destroy(ctx context.Context) error {
	in, err := find(ctx, v.inst)
	if err != nil || in == nil {
		return err
	}
	_, err = provider.Exec(ctx, install, "limactl", "delete", "--force", v.inst)
	return err
}

// Transport is the system ssh, through the client's generated config, which
// includes Lima's ssh.config. The config is read again on each start,
// because Lima may pick a new local port.
func (v *vm) Transport(ctx context.Context, interactive bool) (client.Transport, error) {
	h := v.h
	in := v.last
	if in == nil {
		in, _ = find(ctx, v.inst)
	}
	if in != nil && in.SSHConfigFile != "" {
		h.SSHConfig = in.SSHConfigFile
	}
	if h.SSH == "" {
		h.SSH = sshAlias(v.inst)
	}
	return &client.SSH{Host: h, Interactive: interactive}, nil
}
