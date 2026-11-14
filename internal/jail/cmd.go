package jail

import (
	"io"
	"os"
	"strconv"
	"syscall"
)

// commander is the method set of Cmd, the same on every OS. Off Linux,
// each method returns the error that jails need Linux.
type commander interface {
	Start() error
	Wait() error
	Run() error
	CombinedOutput() ([]byte, error)
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.ReadCloser, error)
	StderrPipe() (io.ReadCloser, error)
	Signal(os.Signal) error
	Pid() int
	ExitCode() int
}

var _ commander = (*Cmd)(nil)

// ExitError says how a jail's command ended when it did not exit with 0.
// A jail whose set-up failed exits with code 125, and says why on stderr.
type ExitError struct {
	Code   int            // the exit code, or -1 when a signal ended the command
	Signal syscall.Signal // the signal that ended the command, or 0
}

// setupFailed is the exit code of a jail whose set-up failed before its
// command ran, as Docker's 125. The reason goes to the command's stderr.
const setupFailed = 125

func (e *ExitError) Error() string {
	switch {
	case e.Signal != 0:
		return "the jailed command ended with signal " + e.Signal.String()
	case e.Code == setupFailed:
		return "the jail could not start its command (exit code 125): the reason is on its stderr"
	default:
		return "the jailed command exited with code " + strconv.Itoa(e.Code)
	}
}
