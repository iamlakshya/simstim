//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// The copied test executable enters the same literal raw image producer
// before testing's flag parser. This hook supplies no session or root grants.
func init() {
	if os.Getenv("NATIVE_ORDINARY_PURGE_IMAGE_TEST") != "1" || len(os.Args) != 2 || os.Args[1] != "purge-git-worker-v3" {
		return
	}
	image, err := adoptRawOrdinaryPurgeGitImageV3()
	if err != nil {
		os.Exit(71)
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: os.Getenv("NATIVE_ORDINARY_PURGE_IMAGE_ADDRESS"), Net: "unix"})
	if err != nil {
		os.Exit(72)
	}
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		os.Exit(73)
	}
	_ = line.SetDeadline(time.Now().Add(10 * time.Second))
	if err := writeOrdinaryGitWorkerHelloV3(context.Background(), line, proto.Hello{Role: ordinaryGitWorkerRoleV2}, image); err != nil {
		os.Exit(74)
	}
	frame, err := line.ReadFrame()
	if err != nil || frame.Type != proto.TPing || line.RejectAncillary() != nil {
		os.Exit(75)
	}
	if errors.Join(line.Close(), image.Close()) != nil {
		os.Exit(76)
	}
	os.Exit(0)
}

func TestOrdinaryPurgePublicBytesRejectDifferentCodeAndDescriptorRolesV3(t *testing.T) {
	directory := t.TempDir()
	originalPath, copiedPath := filepath.Join(directory, "original"), filepath.Join(directory, "copied")
	for _, name := range []string{originalPath, copiedPath} {
		if err := os.WriteFile(name, []byte("original-native-code"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.Open(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	copied, err := os.Open(copiedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	info, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(copiedPath, 0o100); err != nil {
		t.Fatal(err)
	}
	if err := checkPurgePublicCodeBytesV3(t.Context(), original, copied, info); err != nil {
		t.Fatal("actual preopened copy was not readable after sealing", err)
	}
	fd, err := unix.Open(copiedPath, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	opath := os.NewFile(uintptr(fd), "public-opath-code")
	defer opath.Close()
	if err := checkPurgePublicCodeBytesV3(t.Context(), original, opath, info); err == nil {
		t.Fatal("O_PATH public data replaced an actual readable code descriptor")
	}
	if err := os.Chmod(copiedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	writable, err := os.OpenFile(copiedPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if err := checkPurgePublicCodeBytesV3(t.Context(), original, writable, info); err == nil {
		t.Fatal("a writable public descriptor passed as fixed read-only code")
	}
	if err := os.Link(copiedPath, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := checkPurgePublicCodeBytesV3(t.Context(), original, copied, info); err == nil {
		t.Fatal("an aliased public code file was accepted")
	}
	if err := os.Remove(filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := writable.WriteAt([]byte("foreign--native-code"), 0); err != nil {
		t.Fatal(err)
	}
	if err := checkPurgePublicCodeBytesV3(t.Context(), original, copied, info); err == nil {
		t.Fatal("different bytes renewed a copy family from metadata")
	}
}

func TestOrdinaryPurgeImageCannotOriginateFromPlacementOrPublicDataV3(t *testing.T) {
	p := &placement{}
	if image, err := purgeAttemptImageForPlacementV3(t.Context(), p); image != nil || err == nil {
		t.Fatal("a placement-shaped value issued actual copied image custody")
	}
	line := &StartupLineV1{}
	for _, owner := range []*joinedOrdinaryGitInspectionV2{{purgeImage: &purgeAttemptImageV2{}}, {purgeImage: &purgeAttemptImageV2{}, start: &ordinaryGitStartV2{}}} {
		if err := owner.acceptOriginalOrdinaryGitHelloV3(t.Context(), line, proto.Frame{Type: proto.THello}); err == nil {
			t.Fatal("public creator-shaped data admitted a copied fixed worker")
		}
	}
	if line.nativePeer != nil {
		t.Fatal("a refused copied worker produced a native peer")
	}
	for _, image := range []*ordinaryPurgeGitImageV3{nil, {}} {
		if err := image.Check(t.Context()); err == nil {
			t.Fatal("public image-shaped data replaced actual kernel mapping")
		}
	}
}

func TestOrdinaryPurgeRawCopiedImagePreservesGenericSameImageRefusalV3(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the actual execute-only entry class requires a non-root operator")
	}
	original, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := checkPurgeKernelExecEligibilityV2(t.Context(), original); err != nil {
		t.Skipf("native static execute-only fixture unavailable: %v", err)
	}
	copiedPath := filepath.Join(t.TempDir(), "fixed-reader")
	writer, err := os.OpenFile(copiedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, copiedErr := io.Copy(writer, original)
	if err := errors.Join(copiedErr, writer.Close()); err != nil {
		t.Fatal(err)
	}
	copied, err := os.Open(copiedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	if err := os.Chmod(copiedPath, 0o100); err != nil {
		t.Fatal(err)
	}
	parentFD, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	parent := os.NewFile(uintptr(parentFD), "actual-test-original-parent")
	defer parent.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	address := "@ordinary-purge-copy-" + rand.Text()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	deadline, _ := ctx.Deadline()
	_ = listener.SetDeadline(deadline)
	raw, err := listener.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var optionErr error
	if err := raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) }); err != nil || optionErr != nil {
		t.Fatal(errors.Join(err, optionErr))
	}
	descriptor := -1
	command := exec.CommandContext(ctx, "/proc/self/fd/3", "purge-git-worker-v3")
	command.Dir = "/"
	command.Env = append(os.Environ(), "NATIVE_ORDINARY_PURGE_IMAGE_TEST=1", "NATIVE_ORDINARY_PURGE_IMAGE_ADDRESS="+address)
	command.ExtraFiles = []*os.File{copied, parent}
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &descriptor}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if descriptor < 0 {
		t.Fatal("the actual copied reader has no atomic creation pidfd")
	}
	defer unix.Close(descriptor)
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	line, err := NewNativeStartupLineV1(connection)
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	defer line.Close()
	_ = line.SetDeadline(deadline)
	frame, err := line.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if err := line.AcceptNativeHelloV1(frame); err == nil {
		t.Fatal("the generic same-image receiver accepted a different copied inode")
	}
	if line.nativePeer != nil {
		t.Fatal("the generic refusal issued a copied native peer")
	}
	if err := line.WriteFrame(proto.TPing, struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal("actual protected inherited-FD producer/writer failed", err)
	}
}

func TestOrdinaryGitFrozenStartRejectsChangedCommandAndPrivateRolesV3(t *testing.T) {
	code, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer code.Close()
	info, err := code.Stat()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []struct {
		name   string
		change func(*ordinaryGitStartV2)
	}{
		{"command", func(start *ordinaryGitStartV2) { start.command.Args[1] = "holder" }},
		{"roots", func(start *ordinaryGitStartV2) { start.command.ExtraFiles = append(start.command.ExtraFiles, code) }},
		{"environment", func(start *ordinaryGitStartV2) { start.command.Env = nil }},
		{"image", func(start *ordinaryGitStartV2) { start.command.Path = "/bin/sh" }},
		{"attributes", func(start *ordinaryGitStartV2) { start.command.SysProcAttr = &syscall.SysProcAttr{} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			owner := &joinedOrdinaryGitInspectionV2{code: code, creationFD: -1, endpoint: &purgePrivateEndpointV2{}, preparation: &OrdinaryPurgePreparationV2{owner: &ordinaryHolderCaptureV2{origin: &placement{executableRef: info}}}}
			start, err := newOrdinaryGitStartV2(owner)
			if err != nil {
				t.Fatal(err)
			}
			owner.start, owner.command = start, start.command
			if err := start.checkFrozenV2(t.Context()); err != nil {
				t.Fatal("unchanged genuine fixed specification failed", err)
			}
			mutate.change(start)
			if err := start.startActualV2(t.Context()); err == nil {
				t.Fatal("changed fixed specification reached native Start")
			}
			if owner.command.Process != nil || owner.waitDone != nil || owner.creation != nil {
				t.Fatal("refused specification created native lifetime")
			}
		})
	}
}
