// Package lima makes hosts that are local Lima VMs: Ubuntu LTS with systemd
// and sshd, reached with the system ssh through Lima's own generated SSH
// config. It shells out to limactl; Lima has no API.
package lima

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

const install = "Install Lima (https://lima-vm.io/docs/installation/), then run this again."

// Defaults for a new VM: small, and a sparse disk. The sizes are in bytes.
const (
	// DefaultCPUs is the CPU count of a new VM.
	DefaultCPUs = 2
	// DefaultMemory is the memory of a new VM, in bytes.
	DefaultMemory = 4 << 30
	// DefaultDisk is the sparse disk size of a new VM, in bytes.
	DefaultDisk = 40 << 30
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
		Plain                *bool `json:"plain"`
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
	out, err := limaExec(ctx, "list", "--json")
	if err != nil {
		return nil, err
	}
	in, err := parseList(out, name)
	if err != nil {
		return nil, err
	}
	if saved, _ := ctx.Value(limaSavedSourceKey{}).(*limaSavedSource); saved != nil && in != nil {
		if saved.ctx != ctx || saved.owner == nil || in.Name != saved.owner.inst || in.Dir != saved.owner.source.Config.Path || in.SSHConfigFile != filepath.Join(in.Dir, "ssh.config") {
			return nil, plainSourceUnknown()
		}
	}
	if frame, _ := ctx.Value(limaSourceBirthKey{}).(*limaSourceBirth); frame != nil && in != nil {
		if frame.ctx != ctx || frame.locator == nil || in.Dir != filepath.Join(frame.locator.source.Path, name) {
			return nil, plainSourceUnknown()
		}
		if frame.instancePin != nil {
			if err := frame.instancePin.Revalidate(); err != nil {
				return nil, err
			}
		}
	}
	return in, nil
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
	cpus, mem, disk := req.CPUs, req.Memory, req.Disk
	if cpus <= 0 {
		cpus = DefaultCPUs
	}
	if mem <= 0 {
		mem = DefaultMemory
	}
	if disk <= 0 {
		disk = DefaultDisk
	}
	args := []string{"create", "--tty=false", "--name=" + inst, "--plain"}
	if system == "darwin" {
		args = append(args, "--vm-type=vz", "--set=.networkOpts.hostLoopback = false", "--nested-virt")
	}
	return append(args, fmt.Sprintf("--cpus=%d", cpus), "--memory="+gib(mem), "--disk="+gib(disk), Template)
}

// gib formats bytes as the GiB number that limactl accepts.
func gib(bytes int64) string {
	return strconv.FormatFloat(float64(bytes)/(1<<30), 'f', -1, 64)
}

// Create makes and starts the VM, and returns the host that reaches it
// through Lima's ssh.config.
func (Provider) Create(ctx context.Context, req provider.Request) (result client.Host, returned error) {
	scope, err := client.HostCreationSourceScopeContext(ctx)
	if err != nil {
		return client.Host{}, err
	}
	if scope.Request() != provider.CreationSourceRequest("lima", req) {
		return client.Host{}, plainSourceUnknown()
	}
	frame := &limaSourceBirth{scope: scope, request: scope.Request()}
	ctx = context.WithValue(ctx, limaSourceBirthKey{}, frame)
	frame.ctx = ctx
	locator, err := captureLimaSourceLocator()
	if err != nil {
		return client.Host{}, err
	}
	frame.locator = locator
	frame.home = locator.source.Path
	defer func() {
		if frame.inspection != nil {
			returned = errors.Join(returned, frame.inspection.Close())
		}
		returned = errors.Join(returned, frame.instancePin.Close(), locator.close())
		if returned != nil && frame.attempted {
			if result.Machine == nil {
				result = freezeBirthHost(frame.partial)
			}
			returned = provider.RetainedCreation(result, frame.partial.Machine.ID, frame.home, returned)
		}
	}()
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
		return client.Host{}, &client.UserError{Msg: "Lima already has an instance called " + inst, Hint: "Inspect it on the selected original Lima locator, or choose another name.", Code: "instance_exists"}
	}
	if goos != "darwin" {
		fmt.Fprintf(out, "  ! Lima with QEMU cannot stop %s from reaching this machine's loopback services.\n", req.Name)
		fmt.Fprintf(out, "    Try: run untrusted work on a remote host: %s host add\n", brand.Name)
	}
	args := CreateArgs(inst, req, goos)
	fmt.Fprintf(out, "  Creating the VM: limactl %s\n", shellLine(args))
	frame.partial = client.Host{Name: req.Name, Machine: &client.Machine{Provider: "lima", ID: inst}}
	frame.attempted = true
	if _, err := limaExec(ctx, args...); err != nil {
		return client.Host{}, err
	}
	frame.created = true
	instanceSource, instancePin, err := provider.CaptureSourceConfigPinned(filepath.Join(locator.source.Path, inst))
	if err != nil {
		return client.Host{}, err
	}
	if instanceSource.Role != client.MachineConfigExisting {
		return client.Host{}, errors.Join(plainSourceUnknown(), instancePin.Close())
	}
	frame.instanceSource, frame.instancePin = instanceSource, instancePin
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
	if in.Dir != frame.instanceSource.Path || in.SSHConfigFile != filepath.Join(in.Dir, "ssh.config") {
		return client.Host{}, plainSourceUnknown()
	}
	idle := req.IdleStop
	if idle == "" {
		idle = DefaultIdleStop
	}
	h := client.Host{
		Name: req.Name, SSH: sshAlias(inst), SSHConfig: in.SSHConfigFile,
		Machine: &client.Machine{Provider: "lima", ID: inst, IdleStop: idle},
	}
	source, inspection, sourceErr := capturePlainSource(ctx, h, in)
	if sourceErr != nil {
		return client.Host{}, sourceErr
	}
	h.Machine.Source = source
	frame.inspection = inspection
	// Lima's own SSH config checks no host key. The client pins the new
	// VM's key now, before its first connection (isolation spec 14.7.1).
	pin, err := pinHostKey(ctx, h)
	if err != nil {
		return client.Host{}, discard(ctx, inst, fmt.Sprintf("could not pin the host key of the new VM %s (%v)", inst, err), "run this again")
	}
	fmt.Fprintf(out, "  ✓ %s\n", pin.Line())
	frame.result = freezeBirthHost(h)
	return client.RecordHostMachineSourceBirthContext(ctx, h)
}

// pinHostKey pins the host key of a new VM on first use: the client made
// the VM, and nobody else had its key. Tests replace it.
var pinHostKey = func(ctx context.Context, h client.Host) (client.Pin, error) {
	return client.PinHostKey(ctx, h, "", client.FirstUseAccept)
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

// discard reports a failed creation without deleting by a reusable name.
// The caller retains the actual partial resource and original native locator.
func discard(ctx context.Context, inst, problem, hint string) error {
	return &client.UserError{Msg: problem + "; the VM is retained for inspection", Hint: "Inspect the exact VM on its original Lima locator before retrying; " + hint, Code: "lima_vm"}
}

// sshAlias is the Host that Lima's ssh.config defines for an instance.
func sshAlias(inst string) string { return "lima-" + inst }

// Open returns the lifecycle bound to the original instance directory.
func (Provider) Open(h client.Host) (client.Lifecycle, error) {
	if h.Machine == nil || h.Machine.Provider != "lima" {
		return nil, plainSourceUnknown()
	}
	source, err := client.NormaliseMachineSource("lima", h.Machine.Source)
	if err != nil {
		return nil, err
	}
	if source.ResourceID != h.Machine.ID || filepath.Base(source.Config.Path) != source.ResourceID {
		return nil, plainSourceUnknown()
	}
	return &vm{h: freezeBirthHost(h), inst: source.ResourceID, source: source}, nil
}

type vm struct {
	h      client.Host
	inst   string
	source *client.MachineSourceProvenance
}

func state(s string) string {
	switch s {
	case "Running":
		return client.MachineRunning
	case "Installing", "":
		return client.MachineStarting
	default:
		return client.MachineStopped
	}
}
