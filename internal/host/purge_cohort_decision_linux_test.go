//go:build linux

package host

import (
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func nativePurgeDecisionFixtureV3(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	receiver := os.NewFile(uintptr(pair[0]), "real-first-decision-receiver")
	sender := os.NewFile(uintptr(pair[1]), "real-first-decision-sender")
	t.Cleanup(func() { _ = receiver.Close(); _ = sender.Close() })
	return receiver, sender
}

func duplicatePurgeDecisionFixtureV3(t *testing.T, file *os.File) *os.File {
	t.Helper()
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		t.Fatal(err)
	}
	copy := os.NewFile(uintptr(fd), "same-first-decision-native-duplicate")
	t.Cleanup(func() { _ = copy.Close() })
	return copy
}

func TestPurgeCohortFirstNativeProposalIsSharedAndNeverConsumedV3(t *testing.T) {
	receiver, sender := nativePurgeDecisionFixtureV3(t)
	type decisionMonitorFixtureV3 struct{ receiver, sender *os.File }
	monitors := make([]decisionMonitorFixtureV3, 12)
	for i := range monitors {
		monitors[i] = decisionMonitorFixtureV3{duplicatePurgeDecisionFixtureV3(t, receiver), duplicatePurgeDecisionFixtureV3(t, sender)}
	}
	start := make(chan struct{})
	answers := make(chan purgeCohortValueV3, len(monitors))
	failures := make(chan error, len(monitors))
	var joined sync.WaitGroup
	for i, monitor := range monitors {
		joined.Add(1)
		go func(i int, monitor decisionMonitorFixtureV3) {
			defer joined.Done()
			<-start
			proposal := purgeCohortAbortV3
			if i == 0 {
				proposal = purgeCohortCommitV3
			}
			value, err := proposePurgeCohortDecisionV3(monitor.receiver, monitor.sender, proposal)
			if err != nil {
				failures <- err
				return
			}
			answers <- value
		}(i, monitor)
	}
	close(start)
	joined.Wait()
	close(failures)
	close(answers)
	for err := range failures {
		t.Fatal(err)
	}
	first, err := peekPurgeCohortDecisionV3(receiver)
	if err != nil || first == purgeCohortPendingV3 {
		t.Fatal("no actual first queued native proposal", err)
	}
	for answer := range answers {
		if answer != first {
			t.Fatal("different real monitors selected different first decisions", first, answer)
		}
	}
	for range 4 {
		for _, monitor := range monitors {
			value, err := peekPurgeCohortDecisionV3(monitor.receiver)
			if err != nil || value != first {
				t.Fatal("the first decision was consumed or replaced", value, err)
			}
		}
	}
}

func TestPurgeCohortAbortBeforeLateReadinessCannotBecomeCommitV3(t *testing.T) {
	receiver, sender := nativePurgeDecisionFixtureV3(t)
	first, err := proposePurgeCohortDecisionV3(receiver, sender, purgeCohortAbortV3)
	if err != nil || first != purgeCohortAbortV3 {
		t.Fatal(err)
	}
	// A second monitor becomes ready only after the first actual abort. It
	// receives the SAME kernel pair and cannot choose a private local policy.
	lateReceiver := duplicatePurgeDecisionFixtureV3(t, receiver)
	lateSender := duplicatePurgeDecisionFixtureV3(t, sender)
	late, err := proposePurgeCohortDecisionV3(lateReceiver, lateSender, purgeCohortCommitV3)
	if err != nil || late != purgeCohortAbortV3 {
		t.Fatal("late coordinator readiness changed the global abort", late, err)
	}
}

func TestPurgeCohortRejectsWrongNativeRoleAndMalformedPacketV3(t *testing.T) {
	receiver, sender := nativePurgeDecisionFixtureV3(t)
	created, err := receiver.Stat()
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := unix.GetsockoptUint64(int(receiver.Fd()), unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkPurgeDecisionFileV3(receiver, created, cookie); err != nil {
		t.Fatal(err)
	}
	other, _ := nativePurgeDecisionFixtureV3(t)
	if err := checkPurgeDecisionFileV3(other, created, cookie); err == nil {
		t.Fatal("another actual socket acquired the original pair's identity")
	}
	if _, err := unix.SendmsgN(int(sender.Fd()), []byte{byte(purgeCohortCommitV3), byte(purgeCohortAbortV3)}, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if _, err := peekPurgeCohortDecisionV3(receiver); err == nil {
		t.Fatal("a malformed multi-byte packet selected a native decision")
	}
	if _, err := proposePurgeCohortDecisionV3(receiver, sender, purgeCohortAbortV3); err == nil {
		t.Fatal("unknown native state defaulted to abort instead of retaining ownership")
	}
}
