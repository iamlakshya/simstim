package supervisor

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// processEnv is the environment that process pid started with, one
// variable a line.
func processEnv(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\x00", "\n")
}
