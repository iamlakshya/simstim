package lima

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// LoopbackName is the name of the doctor's check of a Lima VM's loopback
// path.
const LoopbackName = "laptop loopback"

// LoopbackCheck is the doctor's check, on this computer, that the Lima VM
// behind h cannot reach this computer's loopback services. It passes only
// for a vz VM with networkOpts.hostLoopback set to false, in settings that
// the running VM started with.
func LoopbackCheck(ctx context.Context, h client.Host) api.Check {
	if h.Machine == nil {
		return api.Check{Name: LoopbackName, Detail: h.Name + " was not made by host create lima", Fix: "limactl list"}
	}
	inst := h.Machine.ID
	in, err := find(ctx, inst)
	if err != nil {
		return api.Check{Name: LoopbackName, Detail: "could not read the VM from limactl: " + err.Error(), Fix: "limactl list --json"}
	}
	c := loopbackCheck(h.Name, inst, in)
	if in == nil || in.VMType != "vz" {
		return c
	}
	if !c.OK && ownLoopbackClosed(in.Dir) {
		// The VM's own lima.yaml closes the path, so override.yaml opens it.
		c.Fix = fmt.Sprintf("remove networkOpts.hostLoopback from %s, then limactl stop %s, then limactl start %s", overridePath(), inst, inst)
	}
	if in.Status == "Running" && changedSinceStart(in) {
		// limactl lists the settings that Lima loads now. A running VM
		// keeps the network that it started with (pkg/driver/vz/vm_darwin.go),
		// so the check says so in either direction.
		restart := "the Lima settings of " + h.Name + " changed after its VM started, so they apply only after a restart"
		if c.OK {
			c.OK, c.Detail = false, restart
			c.Fix = fmt.Sprintf("limactl stop %s, then limactl start %s", inst, inst)
		} else {
			// The Try that closes the path ends with a restart already.
			c.Detail = restart + ", and they let " + h.Name + " reach services on your Mac's loopback address"
		}
	}
	if name, ok := userV2(in); ok && !c.OK {
		// Lima's vz driver refuses hostLoopback false with a user-v2
		// network, so a start without the network removed would fail.
		c.Fix = fmt.Sprintf("remove the network {lima: %s} from networks in %s, then %s", name, networksSource(in, name), c.Fix)
	}
	return c
}

// changedSinceStart reports whether a file that sets a running VM's
// settings changed after its host agent started: the VM's lima.yaml, or
// Lima's default.yaml or override.yaml, or their folder, which changes when
// one of them is added or removed. The host agent's start is the time of
// ha.pid, or of ha.sock. When neither is there, it reports true.
func changedSinceStart(in *instance) bool {
	if in.Dir == "" {
		return true
	}
	start, ok := modTime(filepath.Join(in.Dir, "ha.pid"))
	if !ok {
		start, ok = modTime(filepath.Join(in.Dir, "ha.sock"))
	}
	if !ok {
		return true
	}
	config := filepath.Join(filepath.Dir(in.Dir), "_config")
	for _, f := range []string{filepath.Join(in.Dir, "lima.yaml"), config, filepath.Join(config, "default.yaml"), filepath.Join(config, "override.yaml")} {
		if t, ok := modTime(f); ok && t.After(start) {
			return true
		}
	}
	return false
}

func modTime(path string) (time.Time, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// ownLoopbackClosed reports whether the VM's own lima.yaml, in dir, sets
// networkOpts.hostLoopback to false. A file that is not YAML counts as
// open.
func ownLoopbackClosed(dir string) bool {
	if dir == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "lima.yaml"))
	if err != nil {
		return false
	}
	var y struct {
		NetworkOpts struct {
			HostLoopback *bool `yaml:"hostLoopback"`
		} `yaml:"networkOpts"`
	}
	if err := yaml.Unmarshal(b, &y); err != nil {
		return false
	}
	return y.NetworkOpts.HostLoopback != nil && !*y.NetworkOpts.HostLoopback
}

// loopbackCheck judges one instance of `limactl list --json`.
func loopbackCheck(host, inst string, in *instance) api.Check {
	c := api.Check{Name: LoopbackName}
	switch {
	case in == nil:
		c.Detail, c.Fix = "Lima has no instance called "+inst, "limactl list"
	case in.VMType != "vz":
		driver := in.VMType
		switch driver {
		case "qemu":
			driver = "QEMU"
		case "":
			driver = "an unknown VM type"
		}
		c.Detail = fmt.Sprintf("Lima with %s cannot stop %s from reaching this machine's loopback services", driver, host)
		c.Fix = "run untrusted work on a remote host: " + brand.Name + " host add"
		if goos == "darwin" {
			// On a Mac, a new VM uses vz, which closes the path.
			c.Fix = brand.Name + " host destroy " + host + ", then " + brand.Name + " host create lima " + host
		}
	case closed(in):
		c.OK, c.Detail = true, host+" cannot reach services on your Mac's loopback address"
	default:
		// Lima leaves hostLoopback true by default, and older Lima versions omit it.
		c.Detail = host + " can reach services on your Mac's loopback address"
		c.Fix = fmt.Sprintf("limactl stop %s, then limactl edit --set '.networkOpts.hostLoopback = false' %s, then limactl start %s", inst, inst, inst)
	}
	return c
}

// closed reports whether an instance keeps its VM away from this computer's
// loopback services: vz, with networkOpts.hostLoopback false.
func closed(in *instance) bool {
	return in.VMType == "vz" && in.Config != nil && in.Config.NetworkOpts.HostLoopback != nil && !*in.Config.NetworkOpts.HostLoopback
}

// checkClosed checks a VM that Create made on a Mac, before it first
// starts. Lima's override.yaml wins over the arguments of `limactl create`
// (pkg/limayaml/defaults.go:901-908 in Lima 2.3.0), so it can set vmType or
// hostLoopback back. A VM that is not closed is deleted.
func checkClosed(ctx context.Context, inst string) error {
	in, err := find(ctx, inst)
	if err != nil {
		return discard(ctx, inst, fmt.Sprintf("could not read the new VM %s from limactl (%v)", inst, err), "run this again")
	}
	if in == nil {
		return &client.UserError{Msg: "limactl did not list the new VM " + inst, Hint: "limactl list"}
	}
	if closed(in) {
		// Lima's vz driver refuses hostLoopback false with a user-v2
		// network (pkg/driver/vz/vz_driver_darwin.go:263), which
		// default.yaml or override.yaml can add.
		if name, ok := userV2(in); ok {
			return discard(ctx, inst,
				fmt.Sprintf("Lima gives %s the user-v2 network %s, which cannot go with networkOpts.hostLoopback false", inst, name),
				fmt.Sprintf("remove the network {lima: %s} from networks in %s, then run this again", name, networksSource(in, name)))
		}
		return nil
	}
	setting := "networkOpts.hostLoopback true"
	switch {
	case in.VMType != "vz":
		setting = "vmType " + in.VMType
	case in.Config == nil || in.Config.NetworkOpts.HostLoopback == nil:
		setting = "no networkOpts.hostLoopback"
	}
	return discard(ctx, inst,
		fmt.Sprintf("Lima made %s with %s, which lets it reach services on your Mac's loopback address", inst, setting),
		"remove vmType and networkOpts.hostLoopback from "+overridePath()+", then run this again")
}

// userV2 returns the first of the VM's networks that Lima takes as user-v2.
func userV2(in *instance) (string, bool) {
	if in.Config == nil {
		return "", false
	}
	usernets := userV2Networks(filepath.Join(limaHome(in), "_config", "networks.yaml"))
	for _, n := range in.Config.Networks {
		if usernets[n.Lima] {
			return n.Lima, true
		}
	}
	return "", false
}

// userV2Networks names the networks whose effective mode is user-v2, as
// Lima 2.3.0 works them out. It reads networks.yaml, and writes its default
// file, which defines user-v2, when there is none (pkg/networks/config.go:
// 150-176). When no network has mode user-v2 and a gateway, it puts its
// default user-v2 network in place of any entry called user-v2
// (fillDefaults, config.go:95-113). A network is user-v2 when its mode is
// (Usernet, commands.go:72-77). When it cannot read or parse the file, it
// takes no network as user-v2 (IsUsernet, config.go:217-227).
func userV2Networks(path string) map[string]bool {
	var y struct {
		Networks map[string]struct {
			Mode    string `yaml:"mode"`
			Gateway string `yaml:"gateway"`
		} `yaml:"networks"`
	}
	b, err := os.ReadFile(path)
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil
	case err == nil && yaml.Unmarshal(b, &y) != nil:
		return nil
	}
	usernets := map[string]bool{}
	gateway := false
	for name, n := range y.Networks {
		usernets[name] = n.Mode == "user-v2"
		gateway = gateway || (n.Mode == "user-v2" && n.Gateway != "")
	}
	if !gateway {
		usernets["user-v2"] = true
	}
	return usernets
}

// networksSource names the settings files whose networks list name: the
// VM's own lima.yaml, Lima's override.yaml or its default.yaml. When none
// does, it names the last two.
func networksSource(in *instance, name string) string {
	config := filepath.Join(limaHome(in), "_config")
	files := []string{filepath.Join(config, "override.yaml"), filepath.Join(config, "default.yaml")}
	if in.Dir != "" {
		files = append([]string{filepath.Join(in.Dir, "lima.yaml")}, files...)
	}
	var found []string
	for _, p := range files {
		var y struct {
			Networks []struct {
				Lima string `yaml:"lima"`
			} `yaml:"networks"`
		}
		b, err := os.ReadFile(p)
		if err != nil || yaml.Unmarshal(b, &y) != nil {
			continue
		}
		for _, n := range y.Networks {
			if n.Lima == name {
				found = append(found, p)
				break
			}
		}
	}
	if len(found) == 0 {
		return filepath.Join(config, "default.yaml") + " or " + filepath.Join(config, "override.yaml")
	}
	return strings.Join(found, " and ")
}

// limaHome is the folder of Lima's instances and settings: the parent of
// the instance's folder, else $LIMA_HOME, else ~/.lima.
func limaHome(in *instance) string {
	if in.Dir != "" {
		return filepath.Dir(in.Dir)
	}
	if h := os.Getenv("LIMA_HOME"); h != "" {
		return h
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".lima")
	}
	return ".lima"
}

// overridePath is Lima's file of settings that win over each VM's own:
// $LIMA_HOME/_config/override.yaml, or ~/.lima/_config/override.yaml.
func overridePath() string {
	if h := os.Getenv("LIMA_HOME"); h != "" {
		return filepath.Join(h, "_config", "override.yaml")
	}
	return "~/.lima/_config/override.yaml"
}
