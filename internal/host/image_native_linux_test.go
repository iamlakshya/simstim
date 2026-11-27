//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestImageScopeVacancyDoesNotAcceptAmbiguousKernelDataV1(t *testing.T) {
	for _, tc := range []struct {
		data  string
		empty bool
		bad   bool
	}{
		{"populated 0\nfrozen 0\n", true, false}, {"populated 1\nfrozen 0\n", false, false},
		{"", false, true}, {"frozen 0\n", false, true}, {"populated 0\npopulated 1\n", false, true},
		{"populated 2\n", false, true}, {"populated 0 extra\n", false, true},
	} {
		file, err := os.CreateTemp(t.TempDir(), "native-events-data")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(tc.data); err != nil {
			file.Close()
			t.Fatal(err)
		}
		empty, err := readImageScopeVacancyV1(file)
		file.Close()
		if empty != tc.empty || (err != nil) != tc.bad {
			t.Fatalf("events %q: empty=%v err=%v", tc.data, empty, err)
		}
	}
}
func TestImageGuardianScopeSubprocessCannotInheritPrivateFilesV1(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "original-private-output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, 0); err != nil {
		t.Fatal(err)
	}
	if err := markImageGuardianPrivateV1([]*os.File{file}); err != nil {
		t.Fatal(err)
	}
	cloexec, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || cloexec&unix.FD_CLOEXEC == 0 {
		t.Fatal("scope subprocess can inherit the original output", err)
	}
	after, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || after != flags {
		t.Fatal("private descriptor sealing changed open-file rights", err)
	}
}
func TestImageGuardianNativeZeroStateCannotReleasePlanV1(t *testing.T) {
	if _, err := newImageNativeGuardianV1(context.Background(), &exec.Cmd{}, nil, "other.scope"); err == nil {
		t.Fatal("unrelated command became a native image owner")
	}
	var zero imageNativeGuardianV1
	if err := zero.releasePlanV1(); err == nil {
		t.Fatal("zero native image owner released a converter plan")
	}
	if err := zero.bindScopeV1(context.Background(), imageScopeInfo{}); err == nil {
		t.Fatal("zero native image owner admitted a scope DTO")
	}
}
func TestImageGuardianNativeChildV1(t *testing.T) {
	if os.Getenv("IMAGE_GUARDIAN_NATIVE_CHILD") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}
func TestImageGuardianNativeExitIncludesReapedOriginalPIDFDV1(t *testing.T) {
	fd := -1
	command := exec.Command(os.Args[0], "-test.run=^TestImageGuardianNativeChildV1$")
	command.Env = append(os.Environ(), "IMAGE_GUARDIAN_NATIVE_CHILD=1")
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &fd}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if fd < 0 {
		t.Fatal("actual guardian fixture creation has no pidfd")
	}
	process := os.NewFile(uintptr(fd), "actual-native-guardian-test-creation")
	defer process.Close()
	if ready, err := imageGuardianExitedV1(process); err != nil || ready {
		t.Fatal("live original child became exit evidence", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	if ready, err := imageGuardianExitedV1(process); err != nil || !ready {
		t.Fatal("reaped original creation lost its native exit evidence", err)
	}
}
