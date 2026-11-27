//go:build linux

package none

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestNoneRepeatedKillRetainsTheOriginalTerminalCleanupError(t *testing.T) {
	original := errors.New("original terminal cleanup was uncertain")
	s := &sandbox{closed: true, released: true, started: true, handed: true, reaped: true, known: true, status: syscall.WaitStatus(0), killErr: original}
	for i := 0; i < 2; i++ {
		if err := s.Kill(); !errors.Is(err, original) {
			t.Fatal("repeated cleanup hid its original error", err)
		}
	}
	if s.CleanupCompleteV1() {
		t.Fatal("a failed original cleanup became a stopped-run confirmation")
	}
}

func TestPreparedCleanupCannotConfirmALiveNoneStop(t *testing.T) {
	for _, s := range []*sandbox{
		{closed: true, released: true},
		{closed: true, released: true, started: true, reaped: true, known: true, forced: true, status: syscall.WaitStatus(0)},
	} {
		if s.CleanupCompleteV1() {
			t.Fatal("a pre-handover factory emitted live N5 completion")
		}
	}
	original := &sandbox{closed: true, released: true, started: true, handed: true, reaped: true, known: true, status: syscall.WaitStatus(0)}
	if !original.CleanupCompleteV1() {
		t.Fatal("a complete original live cleanup was refused")
	}
}

func TestNoneFirstCleanupErrorSurvivesItsSecondCall(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	s := &sandbox{root: root, pidfd: -1}
	first := s.Kill()
	if first == nil {
		t.Fatal("an already closed original prepared root reported success")
	}
	if second := s.Kill(); second == nil {
		t.Fatal("a repeated cleanup discarded its first failed native-resource close")
	}
	if s.CleanupCompleteV1() || s.PreparedCleanupCompleteV1() {
		t.Fatal("failed resource closure became complete")
	}
}
