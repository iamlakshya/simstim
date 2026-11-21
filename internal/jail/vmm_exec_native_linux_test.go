//go:build linux

package jail

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

func TestVMMNativeRecordRejectsUnknownAndTrailingFieldsV1(t *testing.T) {
	original := (vmmNativeMessageV1{vmmModeCreateV1, vmmStageInitV1, 1}).bytes()
	for _, change := range []func([]byte) []byte{
		func(data []byte) []byte { return data[:15] },
		func(data []byte) []byte { return append(data, 0) },
		func(data []byte) []byte { data[0] = 'x'; return data },
		func(data []byte) []byte { data[8] = 4; return data },
		func(data []byte) []byte { data[9] = 5; return data },
		func(data []byte) []byte { data[10] = 1; return data },
		func(data []byte) []byte { clear(data[12:]); return data },
	} {
		if _, err := readVMMNativeMessageV1(change(bytes.Clone(original))); err == nil {
			t.Fatal("native record mutation was accepted")
		}
	}
}
func TestVMMNativeProcTaskRejectsAmbiguousBirthV1(t *testing.T) {
	fields := append([]string{"S", "23"}, strings.Fields(strings.Repeat("0 ", 17))...)
	fields = append(fields, "987")
	raw := []byte("41 (a helper ) name) " + strings.Join(fields, " "))
	task, err := parseVMMTaskV1(raw)
	if err != nil || task.pid != 41 || task.parent != 23 || task.birth != 987 {
		t.Fatalf("actual proc comm parsing: %+v, %v", task, err)
	}
	for _, bad := range [][]byte{[]byte("41 helper S 23"), bytes.Replace(raw, []byte(") S "), []byte(") Z "), 1), append(bytes.Clone(raw), bytes.Repeat([]byte{' '}, 8193)...)} {
		if _, err := parseVMMTaskV1(bad); err == nil {
			t.Fatal("ambiguous task identity was accepted")
		}
	}
}
func TestVMMModeZeroSourceCannotAcquireProofV1(t *testing.T) {
	var command Cmd
	source := &supply.ExecModeLeaseV1{}
	if err := command.RetainVMMExecLeaseV1(source); err == nil {
		t.Fatal("zero source and Cmd acquired a mode owner")
	}
	if err := command.WaitVMMNoFutureExecV1(context.Background(), source); err == nil {
		t.Fatal("zero source acquired a no-future proof")
	}
	if err := command.ArmVMMExecModeV1(context.Background(), source); err == nil {
		t.Fatal("zero source acquired an Arm proof")
	}
	if err := command.FinishVMMExecModeV1(context.Background(), source); err == nil {
		t.Fatal("zero source acquired a restoration proof")
	}
}
func TestVMMNativeRegularFileCannotBecomeCreationProofV1(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "ordinary-file")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, _, err := vmmPIDFDIdentityV1(file); err == nil {
		t.Fatal("a regular file became a native creation handle")
	}
}
func TestVMMNativeCreationChildV1(t *testing.T) {
	if os.Getenv("VMM_NATIVE_CREATION_CHILD") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}
func TestVMMNativeActorUsesActualCreationPIDFDV1(t *testing.T) {
	fd := -1
	command := exec.Command(os.Args[0], "-test.run=^TestVMMNativeCreationChildV1$")
	command.Env = append(os.Environ(), "VMM_NATIVE_CREATION_CHILD=1")
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &fd}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	if fd < 0 {
		t.Fatal("actual native creation did not return a pidfd")
	}
	process := os.NewFile(uintptr(fd), "actual-mode-proof-test-creation")
	actor, err := captureVMMActorV1(process)
	if err != nil {
		process.Close()
		t.Fatal(err)
	}
	defer actor.close()
	if actor.pid != command.Process.Pid {
		t.Fatalf("native actor PID %d differs from actual creator %d", actor.pid, command.Process.Pid)
	}
	state, err := readVMMTaskV1(actor.task)
	if err != nil || state.parent != os.Getpid() {
		t.Fatalf("actual native creator ancestry: %+v, %v", state, err)
	}
	if err := actor.live(); err != nil {
		t.Fatal(err)
	}
	if _, namespace, err := vmmPIDFDIdentityV1(process); err != nil || namespace[0] != command.Process.Pid {
		t.Fatalf("actual creation namespace identity: %v, %v", namespace, err)
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := waitOwnedPIDFD(fd, -1); err != nil {
		t.Fatal(err)
	}
	if err := actor.live(); err == nil {
		t.Fatal("positive native exit retained a liveness proof")
	}
}
