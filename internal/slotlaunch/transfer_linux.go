//go:build linux

package slotlaunch

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type nativeTransfer struct {
	files      []*os.File
	allocation bool
}

func (t *nativeTransfer) close() {
	if t != nil {
		for _, file := range t.files {
			file.Close()
		}
	}
}

func dupFile(file *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "original-native-slot"), nil
}

func transferForStart(keeperFD int, init, sealed, cgroup *os.File, runtime []*os.File, disk, wait, self *os.File) (_ *nativeTransfer, returned error) {
	t := &nativeTransfer{allocation: true}
	defer func() {
		if returned != nil {
			t.close()
		}
	}()
	fd, err := unix.FcntlInt(uintptr(keeperFD), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	t.files = append(t.files, os.NewFile(uintptr(fd), "root-created-keeper"))
	for _, file := range append(append([]*os.File{init, cgroup, sealed}, runtime...), disk, wait, self) {
		copy, err := dupFile(file)
		if err != nil {
			return nil, err
		}
		t.files = append(t.files, copy)
	}
	// Capture the sender's own native handle while this exact executable
	// runs. The receiver compares it with kernel SCM_CREDENTIALS, never the
	// socketpair's creation-time SO_PEERCRED or a sudo monitor's PID.
	sender, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return nil, err
	}
	t.files = append(t.files, os.NewFile(uintptr(sender), "root-native-sender"))
	return t, nil
}

// reject ends only the SAME root-created native allocation when a source
// handoff fails. No session/PID lookup can select a later replacement.
func (t *nativeTransfer) reject() {
	if t == nil || !t.allocation || len(t.files) != 13 {
		return
	}
	if fd, err := unix.Openat(int(t.files[2].Fd()), "cgroup.kill", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0); err == nil {
		_, _ = unix.Write(fd, []byte("1"))
		_ = unix.Close(fd)
	}
	_ = unix.PidfdSendSignal(int(t.files[0].Fd()), unix.SIGKILL, nil, 0)
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		live, err := populated(t.files[2])
		if err == nil && !live && pidExited(t.files[0]) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An unproved cgroup stays in its protected allocation record. Future
	// starts quarantine it rather than calling a freed lock proof of exit.
}

func nativeSocket(output io.Writer) bool {
	file, ok := output.(*os.File)
	if !ok || file.Fd() != 1 {
		return false
	}
	_, err := unix.GetsockoptInt(1, unix.SOL_SOCKET, unix.SO_TYPE)
	return err == nil
}

func sendNativeResponse(response Response) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	var descriptors []int
	for _, file := range response.native.files {
		descriptors = append(descriptors, int(file.Fd()))
	}
	if err := unix.Sendmsg(1, data, unix.UnixRights(descriptors...), nil, unix.MSG_NOSIGNAL); err != nil {
		return err
	}
	// Keep the sender/executable chain alive until the receiver has checked
	// its actual current credentials and creation handle.
	var ack [1]byte
	n, err := unix.Read(0, ack[:])
	if err != nil || n != 1 || ack[0] != 1 {
		return refused("the native receiver did not retain its original root source")
	}
	return nil
}
