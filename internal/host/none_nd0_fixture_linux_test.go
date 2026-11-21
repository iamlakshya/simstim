//go:build linux

package host_test

import (
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/secretmem"
)

func init() {
	if len(os.Args) != 2 || os.Args[1] != "none-nd0-fixture-v1" {
		return
	}
	if err := secretmem.Protect(); err != nil {
		os.Exit(73)
	}
	ready := os.NewFile(3, "none-nd0-ready")
	lifetime := os.NewFile(4, "none-nd0-lifetime")
	if ready == nil || lifetime == nil {
		os.Exit(74)
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(75)
	}
	_ = ready.Close()
	_, err := io.Copy(io.Discard, lifetime)
	_ = lifetime.Close()
	if err != nil {
		os.Exit(76)
	}
	os.Exit(0)
}

// The test owns and joins this exact process. Its command has no target
// session role; unreadable ND0 executable state must never associate it.
func startUnrelatedNoneND0FixtureV1(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lifetime, release, err := os.Pipe()
	if err != nil {
		read.Close()
		write.Close()
		t.Fatal(err)
	}
	command := exec.Command(executable, "none-nd0-fixture-v1")
	command.ExtraFiles = []*os.File{write, lifetime}
	if err := command.Start(); err != nil {
		read.Close()
		write.Close()
		lifetime.Close()
		release.Close()
		t.Fatal(err)
	}
	_ = write.Close()
	_ = lifetime.Close()
	t.Cleanup(func() {
		_ = release.Close()
		_ = read.Close()
		joined := make(chan error, 1)
		go func() { joined <- command.Wait() }()
		select {
		case err := <-joined:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-joined
			t.Error("the owned ND0 fixture did not join after its lifetime closed")
		}
	})
	_ = read.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready [1]byte
	if _, err := io.ReadFull(read, ready[:]); err != nil || ready[0] != 1 {
		t.Fatal("the actual unrelated ND0 fixture did not start", err)
	}
}
