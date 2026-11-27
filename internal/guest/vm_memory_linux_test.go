//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestVMGuestBudgetUsesWholeMemTotalWithoutCarveout(t *testing.T) {
	value, err := vmMemTotal(strings.NewReader("MemFree: 9 kB\nMemTotal: 1048576 kB\n"))
	if err != nil || value != 1<<30 {
		t.Fatal("guest RAM was reduced or misparsed", value, err)
	}
	for _, input := range []string{
		"MemTotal: -1 kB\n", "MemTotal: 0 kB\n", "MemTotal: 999999999999999999 kB\n",
		"MemTotal: 9007199254740992 kB\n", "MemTotal: 1 MB\n", "MemFree: 9 kB\n",
		"MemTotal: 1 kB\nMemTotal: 2 kB\n",
	} {
		if _, err := vmMemTotal(strings.NewReader(input)); err == nil {
			t.Fatal("invalid MemTotal accepted", input)
		}
	}
	root := t.TempDir()
	writes := map[string]string{}
	m := &vmMemory{root: root, total: value, write: func(name string, data []byte, _ os.FileMode) error { writes[name] = string(data); return nil }}
	if err := m.configure(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"memory.max": "1073741824", "memory.swap.max": "0", "memory.oom.group": "0", "cgroup.subtree_control": "+memory"} {
		if writes[filepath.Join(root, "agents", name)] != want {
			t.Fatal("guest budget/group changed", name, writes)
		}
	}
}

func TestVMHolderPriorityRestoresAfterFailedSpawn(t *testing.T) {
	var steps []string
	m := &vmMemory{write: func(_ string, data []byte, _ os.FileMode) error { steps = append(steps, string(data)); return nil }}
	failure := fmt.Errorf("start failed")
	if err := m.holderStart(func() error { steps = append(steps, "start"); return failure }); err != failure {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"-500", "start", "1000"}) {
		t.Fatal("priority was not restored around actual spawn", steps)
	}
}

func TestVMEachOwnedRunHasFreshCgroupFDAndClosedKillPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	var killed string
	m := &vmMemory{root: root, write: func(name string, data []byte, _ os.FileMode) error { killed = name + ":" + string(data); return nil }}
	attr := &syscall.SysProcAttr{}
	file, err := m.member(attr, "abcdefgh", vmAgentCommand)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if !attr.UseCgroupFD || attr.CgroupFD != int(file.Fd()) {
		t.Fatal("spawn not bound before Start")
	}
	if _, err := m.member(&syscall.SysProcAttr{}, "abcdefgh", vmAgentCommand); err == nil {
		t.Fatal("owned cgroup identity reused")
	}
	if err := m.killMember("../foreign"); err == nil {
		t.Fatal("arbitrary cgroup path accepted")
	}
	if err := m.killMember("abcdefgh"); err != nil {
		t.Fatal(err)
	}
	if killed != filepath.Join(root, "agents/abcdefgh/cgroup.kill")+":1" {
		t.Fatal("cleanup killed another group", killed)
	}
}

func TestVMMemoryClaimsReadOwnedCountersAndDoNotInventOnFailure(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "agents")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"memory.current": "4096\n", "memory.events": "low 0\nhigh 2\nmax 3\noom 1\noom_kill 1\noom_group_kill 1\nfuture 9\n"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memory := &vmMemory{root: root, total: 1 << 30}
	claim, err := memory.snapshot()
	if err != nil || claim.Current != 4096 || claim.Max != 1<<30 || claim.Events["oom_kill"] != 1 || len(claim.Events) != 6 {
		t.Fatal("owned guest counters lost", claim, err)
	}
	vm := &vmRuntime{memory: memory, applied: true}
	if vm.memoryClaim() == nil {
		t.Fatal("valid prepared claim omitted")
	}
	if err := os.WriteFile(filepath.Join(directory, "memory.current"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if vm.memoryClaim() != nil {
		t.Fatal("diagnostic failure fabricated a zero metric")
	}
}

func TestVMGroupIdentityCannotBeReusedAfterLeafRemovalOrCounterOverflow(t *testing.T) {
	vm := &vmRuntime{}
	seen := map[string]bool{}
	for range 1000 {
		id, err := vm.newGroupID()
		if err != nil {
			t.Fatal(err)
		}
		if !api.ValidSessionID(id) || seen[id] {
			t.Fatal("owned cgroup identity was reused or invalid", id)
		}
		seen[id] = true
	}
	vm.nextGroup = ^uint64(0)
	if _, err := vm.newGroupID(); err == nil {
		t.Fatal("owned cgroup identity counter wrapped")
	}
}

func TestVMDockerDaemonIsLeafAndContainersRemainUnderAggregateCap(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	writes := map[string]string{}
	memory := &vmMemory{root: root, write: func(name string, data []byte, _ os.FileMode) error { writes[name] = string(data); return nil }}
	attr := &syscall.SysProcAttr{}
	file, err := memory.member(attr, "docker", vmDockerCommand)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := os.Stat(filepath.Join(root, "agents/docker/daemon"))
	if err != nil || !os.SameFile(info, daemon) {
		t.Fatal("Docker process was placed in an interior cgroup", err)
	}
	for name, want := range map[string]string{"docker/memory.oom.group": "0", "docker/cgroup.subtree_control": "+memory", "docker/containers/memory.oom.group": "0", "docker/containers/cgroup.subtree_control": "+memory", "docker/daemon/memory.oom.group": "1"} {
		if writes[filepath.Join(root, "agents", name)] != want {
			t.Fatal("Docker hierarchy lost cap/leaf controller contract", name, writes)
		}
	}
}
