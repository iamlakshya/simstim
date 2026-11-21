//go:build linux

package process

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// This exercises the native pidfd/pipe observer only. It is not evidence for
// the actual namespace launcher's C producer or a complete sandbox lifecycle.
func creatorObserverFixtureV3(t *testing.T, script string) (*processCreatorClosureV3, *exec.Cmd, *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	completionFD := int(read.Fd())
	if err := unix.SetNonblock(completionFD, true); err != nil {
		t.Fatal(err)
	}
	fd := -1
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.ExtraFiles = []*os.File{write}
	cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &fd}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if fd < 0 {
		t.Fatal("native kernel did not return original atomic creator pidfd")
	}
	creator := &processCreatorClosureV3{completion: read, completionFD: completionFD, pidfd: fd}
	t.Cleanup(func() {
		_ = write.Close()
		_ = read.Close()
		if creator.pidfd >= 0 {
			_ = unix.Close(creator.pidfd)
		}
	})
	return creator, cmd, write
}

func TestOriginalCreatorRequiresExactCompletionAndNoInheritedWriter(t *testing.T) {
	creator, cmd, writer := creatorObserverFixtureV3(t, "printf C >&3")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if ready, err := creator.check(); err != nil || ready {
		t.Fatal("still-open original writer was treated as complete", ready, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if ready, err := creator.check(); err != nil || !ready {
		t.Fatal("exact native completion and pidfd exit did not settle", ready, err)
	}
	if err := creator.close(); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalCreatorMissingOrExtraCompletionRemainsUnproved(t *testing.T) {
	for _, script := range []string{"exit 0", "printf X >&3", "printf CC >&3", "printf XC >&3"} {
		t.Run(script, func(t *testing.T) {
			creator, cmd, writer := creatorObserverFixtureV3(t, script)
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if ready, _ := creator.check(); ready {
					t.Fatal("missing/extra native receipt became closure")
				}
			}
			if err := creator.close(); err == nil {
				t.Fatal("unknown creator released original handles")
			}
		})
	}
}

func TestOriginalCreatorReadableEventfdCannotReplaceNativePidfd(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	if _, err := write.Write([]byte{'C'}); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Eventfd(1, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	creator := &processCreatorClosureV3{completion: read, completionFD: int(read.Fd()), pidfd: fd}
	if ready, err := creator.check(); err == nil || ready {
		t.Fatal("unrelated readable native object minted creator exit", ready, err)
	}
}
