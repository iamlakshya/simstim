//go:build linux || darwin

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/api"
)

// The low descriptor limit applies only to an owned test child, never the
// shared test runner. This is a real file-planner witness, not a sandbox proof.
func TestCopyPlannerDoesNotRetainOneFDPerFile(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestCopyPlannerFDChild$")
	command.Env = append(os.Environ(), "COPY_PLANNER_FD_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bounded descriptor child: %v: %s", err, output)
	}
}

func TestCopyPlannerFDChild(t *testing.T) {
	if os.Getenv("COPY_PLANNER_FD_CHILD") != "1" {
		return
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	limit.Cur = min(limit.Max, 128)
	if limit.Cur < 64 {
		t.Skip("the caller already has an unusually small descriptor hard limit")
	}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for index := 0; index < 512; index++ {
		if err := os.WriteFile(filepath.Join(directory, "file-"+strconv.Itoa(index)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := buildCopyPlan(t.Context(), []CopySource{{Host: directory, Guest: "/opt/skills"}}, directory, api.DefaultCopyMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.files) != 512 {
		t.Fatal("the planner silently lowered the transferred file count")
	}
}
