package e2b

import (
	"os"
	"os/exec"
	"testing"
)

// drainPipe is unused on Windows, where the fake's shell tests skip.
func drainPipe(*os.File, *[]byte) {}

// leadGroup does nothing on Windows. The fake envd's tests skip there, because
// the fake runs /bin/sh.
func leadGroup() (int, error) { return 0, nil }

// keepFromCommand does nothing on Windows.
func keepFromCommand(...int) {}

// useTestStderr closes the wrapper's stderr, which is the command's.
func useTestStderr(int) { _ = os.Stderr.Close() }

// endGroup kills the command.
func endGroup(cmd *exec.Cmd, _ int) {
	_ = cmd.Process.Kill()
}

// checkDroppedStreamEscapedChild skips on Windows, where the fake envd skips.
func checkDroppedStreamEscapedChild(t *testing.T) {
	t.Skip("the fake envd runs /bin/sh")
}
