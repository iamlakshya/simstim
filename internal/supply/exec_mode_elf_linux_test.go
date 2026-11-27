//go:build linux

package supply

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestExecModeNativeELFWrapperV1(t *testing.T) {
	if os.Getenv("MODE_NATIVE_TEST_ROLE") != "elf-wrapper" {
		return
	}
	if os.Getenv("FSVERITY_NATIVE_FIXTURE") == "" || os.Geteuid() == 0 {
		t.Fatal("native ELF wrapper requires admitted unprivileged fixture")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("MODE_NATIVE_TEST_DIR"), "fixture-item.json"))
	if err != nil {
		t.Fatal(err)
	}
	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	program, lease, writer := os.NewFile(3, "fixture-original-VMM"), os.NewFile(4, "fixture-original-OFD"), os.NewFile(5, "fixture-final-sentinel")
	if err := WriteVMMExecProjectionV1(t.Context(), writer, program, lease, item); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if n, err := os.Stdin.Read(one[:]); err != nil || n != 1 || one[0] != 0xa7 {
		t.Fatal("fixture has no original continuation", err)
	}
	if n, err := os.Stdin.Read(one[:]); err != io.EOF || n != 0 {
		t.Fatal("fixture continuation has surplus bytes", err)
	}
	if err := WriteVMMExecArmedV1(t.Context(), writer, program, lease, item); err != nil {
		t.Fatal(err)
	}
	for _, file := range []*os.File{program, lease, writer} {
		if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			t.Fatal(err)
		}
	}
	// Exactly one actual final external ELF exec. The fixture ELF does NOT
	// set dumpability and reports after the parent restores0555.
	if err := unix.Exec("/proc/self/fd/3", []string{"native-vmm-observer"}, []string{"LANG=C", "LC_ALL=C"}); err != nil {
		t.Fatal(err)
	}
}

// This proves the native immutable-inode/kernel-ordering primitive only.
// Actual H15 projection, private Cmd/code/adoption and full VM availability
// are separate required proofs and are never inferred from this fixture.
func TestNativeELFIsSameMeasuredInodeAndStaysNoDumpAfterRestore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("native NoDump ELF requires unprivileged admitted runner; root is not substituteproof")
	}
	f := modeNativeFixtureV1(t, true)
	owner := f.owner(t)
	program, err := owner.File()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := owner.BeginExecMode(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	loan, err := lease.RetainForJailV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loan.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	armReader, armWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer armReader.Close()
	defer armWriter.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestExecModeNativeELFWrapperV1$")
	command.Env = append(os.Environ(), "MODE_NATIVE_TEST_ROLE=elf-wrapper", "MODE_NATIVE_TEST_DIR="+f.dir)
	command.ExtraFiles = []*os.File{program, loan, writer}
	command.Stdin = armReader
	command.WaitDelay = time.Second
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	created := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &created}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	joined := false
	wait := func() error { joined = true; return command.Wait() }
	defer func() {
		if !joined {
			command.Process.Kill()
			_ = wait()
		}
	}()
	if created < 0 {
		t.Fatal("native fixture lacks original creation handle")
	}
	defer unix.Close(created)
	projection, err := ReadExecModeRecordV1(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.CheckProjectionRecordV1(t.Context(), projection); err != nil {
		t.Fatal(err)
	}
	// The original actual wrapper has adopted both private roles. Parent
	// copies do not keep EOF or the OFD fence alive to VM lifetime.
	writer.Close()
	loan.Close()
	armReader.Close()
	if err := lease.ArmV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := armWriter.Write([]byte{0xa7}); err != nil {
		t.Fatal(err)
	}
	armWriter.Close()
	armed, err := ReadExecModeRecordV1(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.CheckArmedRecordV1(t.Context(), armed); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if n, err := reader.Read(one[:]); n != 0 || err != io.EOF {
		t.Fatal("original final sentinel did not end", n, err)
	}
	if err := lease.RestoreFinalV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatal("actual ELF observer failed", err, output.String())
	}
	var report struct{ Dumpable, Dev, Ino, Mode uint64 }
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err, output.String())
	}
	var original unix.Stat_t
	if err := unix.Fstat(int(f.file.Fd()), &original); err != nil {
		t.Fatal(err)
	}
	if report.Dumpable != 0 || report.Mode != 0o555 || report.Dev != uint64(original.Dev) || report.Ino != uint64(original.Ino) {
		t.Fatal("native ELF was copied, dumpable, or observed before true restoration", report)
	}
}
