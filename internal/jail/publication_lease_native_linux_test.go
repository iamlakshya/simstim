//go:build linux

package jail

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/publicationlease"
	"golang.org/x/sys/unix"
)

// This is a real first-exec/thread-filter falsifier, not whole-jail or
// original-operation acceptance. Only the admitted Linux fixture runs it.
func TestPublicationNativeFirstExecAndEveryWorkerThreadV1(t *testing.T) {
	const key = "JAIL_PUBLICATION_FIXTURE_STAGE_V1"
	switch os.Getenv(key) {
	case "forbidden":
		os.Exit(77)
	case "first":
		runtime.LockOSThread()
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			os.Exit(71)
		}
		if err := protectPublicationFenceLocksV1(publicationlease.GuardianFDV1); err != nil {
			os.Exit(72)
		}
		env := []string{key + "=worker"}
		if err := unix.Exec("/proc/self/exe", []string{os.Args[0], "-test.run=^TestPublicationNativeFirstExecAndEveryWorkerThreadV1$"}, env); err != nil {
			os.Exit(73)
		}
		os.Exit(74)
	case "worker":
		if err := EnterPublicationConverterV1(); err != nil {
			os.Exit(75)
		}
		done := make(chan bool, 8)
		for range 8 {
			go func() {
				runtime.LockOSThread()
				defer runtime.UnlockOSThread()
				fd := publicationlease.GuardianFDV1
				ok := errors.Is(unix.Close(fd), unix.EPERM)
				_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
				ok = ok && errors.Is(err, unix.EPERM)
				alias, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
				if err != nil {
					done <- false
					return
				}
				ok = ok && errors.Is(unix.Flock(alias, unix.LOCK_UN), unix.EPERM)
				unix.Close(alias)
				err = unix.Exec("/proc/self/exe", []string{os.Args[0], "-test.run=^TestPublicationNativeFirstExecAndEveryWorkerThreadV1$"}, []string{key + "=forbidden"})
				done <- ok && errors.Is(err, unix.EPERM)
			}()
		}
		for range 8 {
			if !<-done {
				os.Exit(76)
			}
		}
		os.Exit(0)
	}
	if os.Getenv("PUBLICATION_LEASE_NATIVE_FIXTURE") != "1" {
		t.Skip("requires the admitted native publication filter fixture")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	source, err := publicationlease.CreateV1(t.Context(), root, cachefs.Domain{Scope: strings.Repeat("a", 64), HostID: strings.Repeat("b", 32)}, strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	loan, err := source.RetainForHelperV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestPublicationNativeFirstExecAndEveryWorkerThreadV1$")
	command.Env = append(os.Environ(), key+"=first")
	command.ExtraFiles = make([]*os.File, publicationlease.GuardianFDV1-2)
	command.ExtraFiles[publicationlease.GuardianFDV1-3] = loan
	result := command.Run()
	loan.Close()
	if err := source.FreezeParentV1(); err != nil {
		t.Fatal(err)
	}
	proof, err := source.ProbeV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.RemoveV1(); err != nil {
		t.Fatal(err)
	}
	if err := source.CloseV1(); err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal("native first exec or per-thread retention failed", result)
	}
}
