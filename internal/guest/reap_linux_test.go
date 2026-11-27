//go:build linux

package guest

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// On a kernel without /proc/self/task/*/children, statChildren reads /proc
// and keeps only this process's own children: neither a grandchild nor PID 1.
func TestStatChildrenListsOnlyOwnChildren(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "cat & echo $!; wait")
	cmd.Stdin = pr
	out, err := cmd.StdoutPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		t.Fatal(err)
	}
	pr.Close()
	t.Cleanup(func() {
		pw.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	grand, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	got := statChildren()
	if !slices.Contains(got, cmd.Process.Pid) {
		t.Fatalf("statChildren() = %v, want direct child %d", got, cmd.Process.Pid)
	}
	if slices.Contains(got, grand) {
		t.Fatalf("statChildren() = %v, includes grandchild %d whose parent is %d", got, grand, cmd.Process.Pid)
	}
	if slices.Contains(got, 1) {
		t.Fatalf("statChildren() = %v, includes PID 1", got)
	}
}
