package jail

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestInitClosesEveryOwnedSetupWrapperBeforeChildRelease(t *testing.T) {
	first, err := os.CreateTemp(t.TempDir(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.CreateTemp(t.TempDir(), "second")
	if err != nil {
		t.Fatal(err)
	}
	firstFD, secondFD := initFixtureFDV1(t, first), initFixtureFDV1(t, second)
	if err := closeInitInheritedV1([]*os.File{first, nil, second}); err != nil {
		t.Fatal(err)
	}
	for _, fd := range []int{firstFD, secondFD} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatalf("PID 1 retained an original setup descriptor %d: %v", fd, err)
		}
	}
	if err := closeInitInheritedV1([]*os.File{first}); err == nil {
		t.Fatal("uncertain descriptor closure was hidden before command release")
	}
}

func TestReadonlyProjectionCannotTurnWriteOnlyFileIntoExecutableReadGrant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "program")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fd := initFixtureFDV1(t, file)
	if _, err := readonlyStageRulesV1(&setup{Files: fd - 2, ExecFD: fd}); err == nil {
		t.Fatal("a write-only descriptor acquired read authority during executable projection")
	}
}

func initFixtureFDV1(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	fd := -1
	if err := raw.Control(func(value uintptr) {
		if value <= math.MaxInt32 {
			fd = int(value)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if fd < 3 {
		t.Fatal("the fixture has no bounded native descriptor")
	}
	return fd
}
