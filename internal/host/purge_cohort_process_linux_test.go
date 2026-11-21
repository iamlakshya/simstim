//go:build linux

package host

import (
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

var purgeCohortFixtureEnvV3 = brand.Env("PURGE_COHORT_FIXTURE_V3")

func TestPurgeCohortCoordinatorFixtureV3(t *testing.T) {
	if os.Getenv(purgeCohortFixtureEnvV3) != "1" {
		return
	}
	ready := os.NewFile(5, "actual-coordinator-readiness")
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(2)
	}
	_ = ready.Close()
	var value [1]byte
	_, _ = os.Stdin.Read(value[:])
	os.Exit(0)
}

func TestPurgeCohortActualCoordinatorDeathBeforeLateMonitorV3(t *testing.T) {
	receiver, sender := nativePurgeDecisionFixtureV3(t)
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyRead.Close()
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputWrite.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestPurgeCohortCoordinatorFixtureV3$")
	command.Env = append(os.Environ(), purgeCohortFixtureEnvV3+"=1")
	command.Stdin = inputRead
	command.ExtraFiles = []*os.File{receiver, sender, readyWrite}
	creationFD := -1
	command.SysProcAttr = &syscall.SysProcAttr{PidFD: &creationFD}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = readyWrite.Close()
	_ = inputRead.Close()
	joined := false
	defer func() {
		if !joined {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	if creationFD < 0 {
		t.Fatal("actual coordinator fixture has no atomic pidfd")
	}
	creation := os.NewFile(uintptr(creationFD), "actual-coordinator-creation-pidfd")
	defer creation.Close()
	if err := readyRead.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var ready [1]byte
	if _, err := io.ReadFull(readyRead, ready[:]); err != nil || ready[0] != 1 {
		t.Fatal("actual coordinator did not reach its native ready point", err)
	}
	firstReceiver := duplicatePurgeDecisionFixtureV3(t, receiver)
	firstSender := duplicatePurgeDecisionFixtureV3(t, sender)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	joined = true
	polls := []unix.PollFd{{Fd: int32(creation.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(polls, 0); err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		t.Fatal("the original coordinator has no positive native exit", err)
	}
	first, err := proposePurgeCohortDecisionV3(firstReceiver, firstSender, purgeCohortAbortV3)
	if err != nil || first != purgeCohortAbortV3 {
		t.Fatal("first actual monitor could not select abort after creator death", err)
	}
	lateReceiver := duplicatePurgeDecisionFixtureV3(t, receiver)
	lateSender := duplicatePurgeDecisionFixtureV3(t, sender)
	late, err := proposePurgeCohortDecisionV3(lateReceiver, lateSender, purgeCohortCommitV3)
	if err != nil || late != first {
		t.Fatal("late monitor split the same native cohort after coordinator death", late, err)
	}
}
