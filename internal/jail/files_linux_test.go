package jail

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func descriptorFlags(t *testing.T, file *os.File, command int) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var syscallErr error
	err = raw.Control(func(fd uintptr) { flags, syscallErr = unix.FcntlInt(fd, command, 0) })
	if err != nil || syscallErr != nil {
		t.Fatalf("descriptor flags: %v, %v", err, syscallErr)
	}
	return flags
}

func TestCaptureKeepsTheOriginalFileAfterCallerCloseAndReuse(t *testing.T) {
	name := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(name, []byte("original capability"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	copy, err := captureFile(file, "inherited file", "Give Spec.Files only open files.")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copy.Close() }()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, name+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(copy)
	if err != nil || string(bytes) != "original capability" {
		t.Errorf("captured file after caller close = %q, %v", bytes, err)
	}
}

func TestCapturePreservesNonblockingFlagsAndSetsCloseOnExec(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	flags := descriptorFlags(t, read, unix.F_GETFL)
	if flags&unix.O_NONBLOCK == 0 {
		t.Fatal("the Go pipe fixture is not nonblocking")
	}
	copy, err := captureFile(read, "Stdin", "Give Stdin an open file.")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copy.Close() }()
	_ = copy.Fd() // os.StartProcess obtains the owned file's descriptor this way
	if got := descriptorFlags(t, read, unix.F_GETFL); got != flags {
		t.Errorf("capturing a pipe changed caller flags from %#x to %#x", flags, got)
	}
	if got := descriptorFlags(t, copy, unix.F_GETFD); got&unix.FD_CLOEXEC == 0 {
		t.Error("the parent's captured descriptor can leak through another exec")
	}
}

func TestCaptureAndConcurrentCallerCloseEitherRetainOrRefuse(t *testing.T) {
	name := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(name, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		file, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		closed := make(chan struct{})
		go func() {
			_ = file.Close()
			close(closed)
		}()
		copy, captureErr := captureFile(file, "inherited file", "Give Spec.Files only open files.")
		<-closed
		if captureErr != nil {
			if copy != nil || !strings.Contains(captureErr.Error(), "closed") || hintOf(captureErr) == "" {
				t.Fatalf("capture racing Close = %v, %v", copy, captureErr)
			}
			continue
		}
		bytes, err := io.ReadAll(copy)
		_ = copy.Close()
		if err != nil || string(bytes) != "owned" {
			t.Fatalf("a successful concurrent capture lost its capability: %q, %v", bytes, err)
		}
	}
}

func TestCapturedIOCleanupKeepsCallerOwnership(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "caller")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	c := lifecycleCmd(t)
	c.Stdin, c.Stdout, c.Stderr = file, file, file
	c.files = []*os.File{file, file}
	files, release, err := c.stdio()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("captured %d inherited files, want two", len(files))
	}
	release()
	release()
	for _, copy := range append(files, c.cmd.Stdin.(*os.File), c.cmd.Stdout.(*os.File), c.cmd.Stderr.(*os.File)) {
		if _, err := copy.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("a captured descriptor remains open after release: %v", err)
		}
	}
	if _, err := file.Stat(); err != nil {
		t.Errorf("releasing captures closed the caller's original: %v", err)
	}
}

func TestPartialIOCaptureFailureReleasesEarlierCaptures(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "open")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, inherited := range []bool{false, true} {
		c := lifecycleCmd(t)
		c.Stdin = file
		if inherited {
			c.files = []*os.File{file, closed}
		} else {
			c.Stdout = closed
		}
		before := openFDs(t)
		files, release, err := c.stdio()
		if err == nil || files != nil || release != nil || hintOf(err) == "" {
			t.Fatalf("partial capture failure = %v, %v", files, err)
		}
		if added := newFDs(before, openFDs(t)); len(added) != 0 {
			t.Errorf("partial capture failure leaked descriptors: %v", added)
		}
		if _, err := file.Stat(); err != nil {
			t.Errorf("failed capture closed the caller's original: %v", err)
		}
	}
}
