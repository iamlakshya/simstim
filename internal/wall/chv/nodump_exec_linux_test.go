//go:build linux

package chv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// This proves the kernel execution boundary with the test ELF. It is separate
// from the required post-exec proof of the actual pinned VMM on native KVM.
func TestExecuteOnlyNoDumpKernelBoundary(t *testing.T) {
	if os.Getenv("WALL_CHV_DUMP_CHILD") == "inspect" {
		value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Dumpable int `json:"dumpable"`
		}{value})
		os.Exit(0)
	}
	if err := noDumpPrerequisite(); err != nil {
		t.Skip("the execution prerequisite is unavailable: " + err.Error())
	}
	var header unix.CapUserHeader
	header.Version = unix.LINUX_CAPABILITY_VERSION_3
	var caps [2]unix.CapUserData
	if err := unix.Capget(&header, &caps[0]); err != nil {
		t.Fatal(err)
	}
	if caps[0].Effective != 0 || caps[1].Effective != 0 {
		t.Skip("the kernel proof requires the same dropped-capability execution contract as the jail")
	}
	name, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	item := supply.NewItem(supply.KindVMM, "cloud-hypervisor", "54.1", runtime.GOARCH, data, supply.ReleaseFolder("1.0.0")+"/cloud-hypervisor-static")
	item.HostKernel, item.Released, item.EOL = ">=6.8", "2026-11-11", "2027-01-06"
	sealed, err := supply.SealRuntime(context.Background(), item, source)
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	for _, mode := range []os.FileMode{0o111, 0o555} {
		if err := sealed.Chmod(mode); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("/proc/self/fd/3", "-test.run=^TestExecuteOnlyNoDumpKernelBoundary$")
		command.ExtraFiles = []*os.File{sealed}
		command.Env = []string{"WALL_CHV_DUMP_CHILD=inspect", "LANG=C"}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("execute unchanged test ELF: %v %s", err, output)
		}
		var result struct {
			Dumpable int `json:"dumpable"`
		}
		want := 0
		if mode == 0o555 {
			want = 1
		}
		if err := json.Unmarshal(output, &result); err != nil || result.Dumpable != want {
			t.Fatalf("mode%o post-exec dumpability: %s %v", mode, strings.TrimSpace(string(output)), err)
		}
	}
}
