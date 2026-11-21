//go:build linux

package guest

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nativememory"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

type vmMemory struct {
	scope *vmMemoryScope
	root  string
	total int64
	write func(string, []byte, os.FileMode) error
}

func vmMemTotal(input io.Reader) (int64, error) {
	info, err := nativememory.ReadMeminfo(context.Background(), input, nativememory.MemTotal)
	if err != nil {
		return 0, err
	}
	total, err := info.Value(nativememory.MemTotal)
	if err != nil || total == 0 || total > math.MaxInt64 {
		return 0, fmt.Errorf("invalid guest MemTotal")
	}
	return int64(total), nil
}

func setupVMMemory(ctx context.Context, mount func(string, string, string, uintptr, string) error) (*vmMemory, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	total, readErr := vmMemTotal(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	root := "/sys/fs/cgroup"
	if err := mount("cgroup2", root, "cgroup2", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return nil, fmt.Errorf("mount guest memory controller: %w", err)
	}
	memory := &vmMemory{root: root, total: total, write: os.WriteFile}
	if err := memory.configure(); err != nil {
		return nil, err
	}
	// Linux oom_unkillable_task excludes global PID1. Fixed1000children
	// inherit this preference; holder starts temporarily inherit-500 under
	// the same init spawn lock, then parent returns to1000. No RAM carveout.
	if err := memory.write("/proc/self/oom_score_adj", []byte("1000"), 0); err != nil {
		return nil, err
	}
	memory.scope, err = openVMMemoryScope(ctx)
	if err != nil {
		return nil, err
	}
	return memory, nil
}

func (m *vmMemory) configure() error {
	if m.total <= 0 {
		return fmt.Errorf("guest memory budget is not positive")
	}
	if err := m.write(filepath.Join(m.root, "cgroup.subtree_control"), []byte("+memory"), 0); err != nil {
		return fmt.Errorf("enable guest memory controller: %w", err)
	}
	aggregate := filepath.Join(m.root, "agents")
	if err := os.Mkdir(aggregate, 0o755); err != nil {
		return err
	}
	for _, setting := range []struct{ name, value string }{{"memory.max", strconv.FormatInt(m.total, 10)}, {"memory.swap.max", "0"}, {"memory.oom.group", "0"}, {"cgroup.subtree_control", "+memory"}} {
		if err := m.write(filepath.Join(aggregate, setting.name), []byte(setting.value), 0); err != nil {
			return fmt.Errorf("set guest%s: %w", setting.name, err)
		}
	}
	return nil
}

// member supplies the actual clone3CgroupFD boundary before any1000child
// runs. The one P113runner still owns Start/signal/unreapedwait/cleanup.
func (m *vmMemory) member(attr *syscall.SysProcAttr, id string, role vmCommandRole) (*os.File, error) {
	if !api.ValidSessionID(id) || role != vmAgentCommand && role != vmSeedCommand && role != vmDockerCommand && role != vmFileCommitCommand {
		return nil, vmError("the guest memory member or role is invalid")
	}
	root, err := os.OpenRoot(filepath.Join(m.root, "agents"))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir(id, 0o755); err != nil {
		return nil, err
	}
	leaf := id
	if role == vmDockerCommand {
		if err := m.dockerGroups(root, id); err != nil {
			return nil, err
		}
		leaf = filepath.Join(id, "daemon")
	} else if err := m.write(filepath.Join(m.root, "agents", id, "memory.oom.group"), []byte("1"), 0); err != nil {
		return nil, err
	}
	info, err := root.Lstat(leaf)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, vmError("the guest memory member is not a real directory")
	}
	file, err := root.Open(leaf)
	if err != nil {
		return nil, err
	}
	current, err := file.Stat()
	if err != nil || !os.SameFile(info, current) {
		file.Close()
		return nil, vmError("the guest memory member changed")
	}
	attr.UseCgroupFD = true
	attr.CgroupFD = int(file.Fd())
	return file, nil
}

// holderStart is called ONLY while the canonical init spawn mutex is held,
// including all exec/member/Docker starts. It does not own a second waiter.
func (m *vmMemory) holderStart(start func() error) error {
	if err := m.write("/proc/self/oom_score_adj", []byte("-500"), 0); err != nil {
		return err
	}
	err := start()
	restore := m.write("/proc/self/oom_score_adj", []byte("1000"), 0)
	if err != nil {
		return err
	}
	return restore
}

func (m *vmMemory) snapshot() (vmMemClaim, error) {
	claim := vmMemClaim{Max: m.total, Events: map[string]uint64{}}
	for _, name := range []string{"memory.current", "memory.events"} {
		text, err := vmReadKernelFile(filepath.Join(m.root, "agents", name))
		if err != nil {
			return claim, err
		}
		if name == "memory.current" {
			value, err := strconv.ParseInt(text, 10, 64)
			if err != nil || value < 0 {
				return claim, fmt.Errorf("invalid claimed guest memory")
			}
			claim.Current = value
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			parts := strings.Fields(line)
			if len(parts) != 2 {
				return claim, fmt.Errorf("invalid claimed guest memory events")
			}
			switch parts[0] {
			case "low", "high", "max", "oom", "oom_kill", "oom_group_kill":
			default:
				continue
			}
			value, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				return claim, err
			}
			claim.Events[parts[0]] = value
		}
	}
	return claim, nil
}

type vmMemClaim struct {
	Current int64             `json:"current"`
	Max     int64             `json:"max"`
	Events  map[string]uint64 `json:"events"`
}

func (m *vmMemory) killMember(id string) error {
	if !api.ValidSessionID(id) {
		return vmError("the owned guest cgroup member is invalid")
	}
	return m.write(filepath.Join(m.root, "agents", id, "cgroup.kill"), []byte("1"), 0)
}

func (v *vmRuntime) memoryClaim() *proto.GuestMemoryV1 {
	v.mu.Lock()
	defer v.mu.Unlock()
	// beginStop cancels then acquires this mutex. That fences every counter
	// descriptor before filesystem teardown; no new read starts afterwards.
	if !v.applied || v.ctx != nil && v.ctx.Err() != nil {
		return nil
	}
	claim, err := v.memory.snapshot()
	if err != nil {
		return nil
	}
	return &proto.GuestMemoryV1{Current: claim.Current, Max: claim.Max, Events: claim.Events}
}

// releaseMember removes only the closed private leaf after the kernel says no
// task remains. It never removes an arbitrary tree or reuses the old name.
func (m *vmMemory) releaseMember(id string) error {
	if !api.ValidSessionID(id) {
		return vmError("the completed guest cgroup member is invalid")
	}
	root, err := os.OpenRoot(filepath.Join(m.root, "agents"))
	if err != nil {
		return err
	}
	defer root.Close()
	deadline := time.Now().Add(time.Second)
	for {
		file, err := root.Open(filepath.Join(id, "cgroup.events"))
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 4097))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > 4096 {
			return vmError("the completed guest cgroup cannot be inspected")
		}
		empty := false
		for _, line := range strings.Split(string(data), "\n") {
			if line == "populated 0" {
				empty = true
			}
		}
		if empty {
			return removeVMGroupDirectories(root, id, root.Remove)
		}
		if time.Now().After(deadline) {
			return vmError("the owned guest cgroup still has tasks after cleanup")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
