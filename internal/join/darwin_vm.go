//go:build darwin

package join

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/limaprofile"
)

func macDirectoryIdentity(path string, uid int) (macFileState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return macFileState{}, err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(native.Uid) != uid || info.Mode().Perm()&0022 != 0 {
		return macFileState{}, macFailure("join_mac_vm", "the original Lima directory has another owner or native type")
	}
	return macFileState{Exists: true, Device: uint64(native.Dev), Inode: native.Ino, Mode: uint32(info.Mode().Perm())}, nil
}

func (e *macExecutor) vmOwner(ctx context.Context, account *macAccount) (*macAccount, error) {
	if e.ledger.Options.StartMode == "daemon" {
		return account, nil
	}
	agent, err := macReadAccount(ctx, e.ledger.AgentUser)
	if err != nil {
		return nil, err
	}
	if agent.UID != e.ledger.AgentUID || agent.GUID != e.ledger.AgentGUID || agent.Home != e.ledger.AgentHome {
		return nil, macFailure("join_mac_agent", "the original launch-agent user changed")
	}
	return agent, nil
}

func (e *macExecutor) limaCommand(ctx context.Context, owner *macAccount, args ...string) ([]byte, error) {
	current, err := macDirectoryIdentity(e.ledger.LimaHome, owner.UID)
	if err != nil || current != e.ledger.LimaRef {
		return nil, macFailure("join_mac_vm", "the original Lima home changed before native execution")
	}
	if _, err := macProtected(e.limaBinary(), false); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, e.limaBinary(), args...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "HOME=" + owner.Home, "LIMA_HOME=" + e.ledger.LimaHome}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(owner.UID), Gid: uint32(owner.GID), Groups: []uint32{uint32(owner.GID)}}}
	command.WaitDelay = 2 * time.Second
	var output macOutput
	command.Stdout, command.Stderr = &output, &output
	err = command.Run()
	if output.overflow {
		return nil, macFailure("join_mac_output", "the pinned Lima command exceeded its output bound")
	}
	if ctx.Err() != nil {
		return output.data, errors.Join(ctx.Err(), err)
	}
	return output.data, err
}

func (e *macExecutor) prepareMacVM(ctx context.Context, account *macAccount) error {
	owner, err := e.vmOwner(ctx, account)
	if err != nil {
		return err
	}
	if !e.ledger.LimaRef.Exists {
		if _, err := e.intent("lima-home", e.ledger.LimaHome); err != nil {
			return err
		}
		if e.ledger.Options.StartMode == "user-agent" {
			base := filepath.Join(owner.Home, "Library/Application Support", brand.Name)
			if err := macMakeUserParents(base, owner); err != nil {
				return err
			}
			if err := macMakeUserParents(filepath.Join(base, "hosts"), owner); err != nil {
				return err
			}
		}
		if err := os.Mkdir(e.ledger.LimaHome, 0700); err != nil {
			return macFailure("join_mac_vm", "the fresh Lima home already exists or could not be created; keep the original ledger")
		}
		if err := os.Chown(e.ledger.LimaHome, owner.UID, owner.GID); err != nil {
			return err
		}
		identity, err := macDirectoryIdentity(e.ledger.LimaHome, owner.UID)
		if err != nil {
			return err
		}
		e.ledger.LimaRef = identity
		if err := e.applied("lima-home"); err != nil {
			return err
		}
	}
	if e.ledger.GuestPort == 0 {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			return err
		}
		e.ledger.GuestPort = uint16(port)
		if err := e.save(); err != nil {
			return err
		}
	}
	if e.ledger.VMRef.Exists {
		actual, err := macDirectoryIdentity(filepath.Join(e.ledger.LimaHome, e.ledger.Instance), owner.UID)
		if err != nil || actual != e.ledger.VMRef {
			return macFailure("join_mac_vm", "the original Mac VM directory was replaced")
		}
		return e.checkMacProfile(ctx, owner)
	}
	profile, err := limaprofile.MacHostYAMLV1(limaprofile.MacHostProfileV1{Account: e.ledger.Account, CPUs: e.ledger.Options.CPUs, MemoryBytes: e.ledger.Options.MemoryBytes, SSHPort: e.ledger.GuestPort, Nested: !e.ledger.NestedDisabled, ImagePath: e.imagePath(), Image: e.plan.image, PublicKey: e.plan.invite.PublicKey})
	if err != nil {
		return err
	}
	if err := e.installFile("profile-input", filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "input.yaml"), profile, 0444); err != nil {
		return err
	}
	if _, err := e.intent("vm", filepath.Join(e.ledger.LimaHome, e.ledger.Instance)); err != nil {
		return err
	}
	if _, err := e.limaCommand(ctx, owner, "create", "--tty=false", "--name="+e.ledger.Instance, filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "input.yaml")); err != nil {
		return fmt.Errorf("create the original Mac VM: %w", err)
	}
	identity, err := macDirectoryIdentity(filepath.Join(e.ledger.LimaHome, e.ledger.Instance), owner.UID)
	if err != nil {
		return err
	}
	e.ledger.VMRef = identity
	if err := e.captureMacProfile(ctx, owner, "profile"); err != nil {
		return err
	}
	return e.applied("vm")
}

func macMakeUserParents(path string, owner *macAccount) error {
	if !strings.HasPrefix(path, owner.Home+string(filepath.Separator)) {
		return macFailure("join_mac_agent", "a user-agent path left its original user's home")
	}
	var missing []string
	for current := path; current != owner.Home; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, current)
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return macFailure("join_mac_agent", "the user-agent home contains another native type")
		}
		if uid := int(info.Sys().(*syscall.Stat_t).Uid); uid != 0 && uid != owner.UID {
			return macFailure("join_mac_agent", "another user owns the user-agent path")
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil {
			return err
		}
		if err := os.Chown(missing[i], owner.UID, owner.GID); err != nil {
			return err
		}
	}
	return nil
}

type macLimaObservation struct {
	Name         string `json:"name"`
	Dir          string `json:"dir"`
	Status       string `json:"status"`
	VMType       string `json:"vmType"`
	SSHLocalPort int    `json:"sshLocalPort"`
	Config       *struct {
		Plain        *bool             `json:"plain"`
		Nested       *bool             `json:"nestedVirtualization"`
		Mounts       []json.RawMessage `json:"mounts"`
		PortForwards []json.RawMessage `json:"portForwards"`
		NetworkOpts  struct {
			HostLoopback *bool `json:"hostLoopback"`
		} `json:"networkOpts"`
		Containerd struct {
			System *bool `json:"system"`
			User   *bool `json:"user"`
		} `json:"containerd"`
	} `json:"config"`
}

func (e *macExecutor) observeMacVM(ctx context.Context, owner *macAccount) (macLimaObservation, error) {
	data, err := e.limaCommand(ctx, owner, "list", "--json", e.ledger.Instance)
	if err != nil {
		return macLimaObservation{}, err
	}
	var item macLimaObservation
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if decoder.Decode(&item) != nil || decoder.Decode(new(any)) != io.EOF || item.Name != e.ledger.Instance || item.Dir != filepath.Join(e.ledger.LimaHome, e.ledger.Instance) || item.SSHLocalPort != int(e.ledger.GuestPort) || item.VMType != "vz" || item.Config == nil {
		return item, macFailure("join_mac_vm", "the native Lima observation names another instance or fixed SSH port")
	}
	c := item.Config
	if c.Plain == nil || !*c.Plain || c.NetworkOpts.HostLoopback == nil || *c.NetworkOpts.HostLoopback || len(c.Mounts) != 0 || len(c.PortForwards) != 0 || c.Containerd.System == nil || *c.Containerd.System || c.Containerd.User == nil || *c.Containerd.User || c.Nested == nil || *c.Nested == e.ledger.NestedDisabled {
		return item, macFailure("join_mac_vm", "the native VM profile enables sharing or differs from its original nesting choice")
	}
	return item, nil
}

func (e *macExecutor) captureMacProfile(ctx context.Context, owner *macAccount, id string) error {
	if _, err := e.observeMacVM(ctx, owner); err != nil {
		return err
	}
	path := filepath.Join(e.ledger.LimaHome, e.ledger.Instance, "lima.yaml")
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
		return macFailure("join_mac_vm", "the native VM profile is unavailable or too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, 256<<10+1))
	if err != nil || len(data) > 256<<10 {
		return macFailure("join_mac_vm", "the native VM profile exceeds its bound")
	}
	return e.installFile(id, filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "profile.yaml"), data, 0444)
}

func (e *macExecutor) checkMacProfile(ctx context.Context, owner *macAccount) error {
	if _, err := e.observeMacVM(ctx, owner); err != nil {
		return err
	}
	original, _, err := macReadFile(filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "profile.yaml"), 256<<10)
	if err != nil {
		return err
	}
	actual, err := os.ReadFile(filepath.Join(e.ledger.LimaHome, e.ledger.Instance, "lima.yaml"))
	if err != nil {
		return err
	}
	if len(actual) > 256<<10 || macDigest(actual) != macDigest(original) {
		return macFailure("join_mac_vm", "the original VM profile changed after preparation")
	}
	for _, name := range []string{"default.yaml", "override.yaml"} {
		if _, err := os.Lstat(filepath.Join(e.ledger.LimaHome, "_config", name)); !errors.Is(err, os.ErrNotExist) {
			return macFailure("join_mac_vm", "an applicable Lima default or override could change the original closed profile")
		}
	}
	return nil
}

func (e *macExecutor) guestHostKeys(ctx context.Context, owner *macAccount) ([]string, error) {
	data, err := e.limaCommand(ctx, owner, "shell", e.ledger.Instance, "--", "/bin/sh", "-c", "cat /etc/ssh/ssh_host_ed25519_key.pub /etc/ssh/ssh_host_ecdsa_key.pub /etc/ssh/ssh_host_rsa_key.pub 2>/dev/null")
	if err != nil {
		return nil, err
	}
	var result []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fingerprint, _, options, rest, err := sshParsePublic([]byte(line))
		if err != nil || len(options) != 0 || len(rest) != 0 {
			return nil, macFailure("join_mac_host_key", "the original VM returned an invalid public host key")
		}
		result = append(result, fingerprint)
	}
	if len(result) == 0 {
		return nil, macFailure("join_mac_host_key", "the original VM has no verified public host key")
	}
	return result, nil
}
