//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fdIdentity struct {
	Device uint64
	Inode  uint64
}

func TestVMMExecClosesProgramHelperAndControlCapabilities(t *testing.T) {
	stage := os.Getenv("WALL_CHV_FD_CHILD")
	if stage == "launch" {
		runtime.LockOSThread()
		if err := closeRuntimeDescriptors(map[int]bool{4: true}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		program, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		if err := unix.Exec(program, []string{program, "-test.run=^TestVMMExecClosesProgramHelperAndControlCapabilities$"}, []string{"WALL_CHV_FD_CHILD=inspect", "WALL_CHV_FD_IDENTITIES=" + os.Getenv("WALL_CHV_FD_IDENTITIES")}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if stage == "inspect" {
		var expected map[int]fdIdentity
		if err := json.Unmarshal([]byte(os.Getenv("WALL_CHV_FD_IDENTITIES")), &expected); err != nil {
			os.Exit(2)
		}
		for fd, identity := range expected {
			var stat unix.Stat_t
			err := unix.Fstat(fd, &stat)
			same := err == nil && uint64(stat.Dev) == identity.Device && stat.Ino == identity.Inode
			if same != (fd == 4) {
				fmt.Fprintln(os.Stderr, "capability survived incorrectly at fd"+strconv.Itoa(fd))
				os.Exit(2)
			}
		}
		os.Exit(0)
	}
	dir := diskTestDir(t)
	var files []*os.File
	expected := map[int]fdIdentity{}
	for i := range 4 {
		file, err := os.CreateTemp(dir, "capability")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		files = append(files, file)
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			t.Fatal(err)
		}
		expected[3+i] = fdIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}
	}
	metadata, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, program, "-test.run=^TestVMMExecClosesProgramHelperAndControlCapabilities$")
	command.ExtraFiles = files
	command.Env = append(os.Environ(), "WALL_CHV_FD_CHILD=launch", "WALL_CHV_FD_IDENTITIES="+string(metadata))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real external exec leaked a capability: %v %s", err, output)
	}
}
