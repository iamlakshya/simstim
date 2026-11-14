package supervisor

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// processEnv is the command line and the environment that process pid
// started with, one string a line, from sysctl kern.procargs2.
func processEnv(t *testing.T, pid int) string {
	t.Helper()
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		t.Fatalf("kern.procargs2 of %d: %v", pid, err)
	}
	return strings.ReplaceAll(string(b[4:]), "\x00", "\n")
}
