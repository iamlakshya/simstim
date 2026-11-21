//go:build linux || darwin

package diag

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMonotonicClockChildV1(t *testing.T) {
	if os.Getenv("DIAG_CLOCK_CHILD_V1") != "1" {
		return
	}
	var one [1]byte
	for {
		if _, err := io.ReadFull(os.Stdin, one[:]); err != nil {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stdout, Now())
	}
}

func TestMonotonicClockIsInsideEveryOriginalCrossProcessBracket(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMonotonicClockChildV1$")
	command.Env = []string{"DIAG_CLOCK_CHILD_V1=1", "HOME=" + home, "USERPROFILE=" + home}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		directory := filepath.Join(home, strings.ToLower(name))
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		command.Env = append(command.Env, name+"="+directory)
	}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = command.Process.Kill(); _ = command.Wait() })
	reader := bufio.NewReader(output)
	for count := 0; count < 1000; count++ {
		before := Now()
		if _, err := input.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		after := Now()
		if err != nil {
			t.Fatal(err)
		}
		child, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil || child < before || child > after {
			t.Fatalf("original cross-process clock escaped bracket: %d <= %d <= %d: %v", before, child, after, err)
		}
	}
}
