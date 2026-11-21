package jail

import (
	"context"
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func TestControllerPublicPurposeCannotEnterLegacyOrArbitraryExec(t *testing.T) {
	public := &mappedControllerPublicV1{ID: "gvisorprobe", Role: "probe-true", CPUs: 1, MemoryMax: 512 << 20, HostMemory: 4 << 30}
	code, extent, err := mappedControllerExtentV1(*public)
	if err != nil {
		t.Fatal(err)
	}
	set := &setup{ControllerPublicV1: public, GvisorV1Version: 1, Userns: true, Net: NetNone, Seccomp: profileGvisorV1, BindsV1Version: 1, Binds: []bind{{SuppliedV1: true}}, Files: extent - 3, ExecFD: code + 1, Argv: []string{"/proc/self/fd/" + strconv.Itoa(code+1), "native-controller-setup-v1"}}
	for stage, normal := range map[string]string{stageInitGvisorControllerV1: stageInit, stageExecGvisorControllerV1: stageExec} {
		got, err := checkFileRulesStageV1(stage, set)
		if err != nil || got != normal {
			t.Fatal("the exact closed public purpose was refused", stage, got, err)
		}
	}
	for _, stage := range []string{stageInit, stageExec, stageOuter, stageInitGvisorV1, stageExecGvisorV1, stageInitBindsV1, stageExecBindsV1} {
		if _, err := checkFileRulesStageV1(stage, set); err == nil {
			t.Fatal("a legacy role adopted the fixed controller vector", stage)
		}
	}
	changed := *set
	changed.Argv = []string{"/bin/sh", "-c", "untrusted"}
	if _, err := checkFileRulesStageV1(stageExecGvisorControllerV1, &changed); err == nil {
		t.Fatal("arbitrary command entered the fixed controller path")
	}
	changed = *set
	changed.ControllerPublicV1 = nil
	if _, err := checkFileRulesStageV1(stageExecGvisorControllerV1, &changed); err == nil {
		t.Fatal("closed controller entry accepted a missing original public purpose")
	}
	for _, change := range []func(*setup){
		func(v *setup) { v.VMMExecV1 = &vmmExecSetupV1{} },
		func(v *setup) { v.PublicationLeaseV1 = &publicationLeaseSetupV1{} },
		func(v *setup) { v.InitLeaseV1Version = 1 },
	} {
		mixed := *set
		change(&mixed)
		if _, err := checkFileRulesStageV1(stageExecGvisorControllerV1, &mixed); err == nil {
			t.Fatal("another private execution purpose entered the controller path")
		}
	}
	changed = *set
	changed.Files++
	if _, err := checkFileRulesStageV1(stageExecGvisorControllerV1, &changed); err == nil {
		t.Fatal("extra inherited descriptor entered the closed controller vector")
	}
}

func TestControllerCopiesAndUnstartedCommandsCannotReleaseGuest(t *testing.T) {
	for _, aggregate := range []*GvisorControllerAggregateV1{nil, {}, {owner: &gvisorControllerOwnerV1{}}} {
		if err := aggregate.ReleaseGuestV1(context.Background()); err == nil {
			t.Fatal("copied role data released an original private guest")
		}
	}
	command := &Cmd{}
	if _, err := command.AwaitGvisorControllerV1(context.Background()); err == nil {
		t.Fatal("unstarted command manufactured a native aggregate")
	}
	if _, err := CommandGvisorControllerCapturedV1(context.Background(), Spec{Argv: []string{"/bin/sh"}}, nil, GvisorControllerInputV1{}, nil); err == nil {
		t.Fatal("caller argv manufactured a fixed controller command")
	}
	if _, err := CommandGvisorControllerCapturedV1(context.Background(), Spec{}, nil, GvisorControllerInputV1{}, nil); err == nil {
		t.Fatal("data without original public native objects manufactured a controller")
	}
}

func controllerPacketPairV1(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	left, right := os.NewFile(uintptr(pair[0]), "controller-packet-left"), os.NewFile(uintptr(pair[1]), "controller-packet-right")
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	for _, fd := range pair {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
			t.Fatal(err)
		}
	}
	return left, right
}

// Transport checks alone issue no namespace, final or private aggregate.
func TestControllerPacketsRequireCurrentSCMAndExactRights(t *testing.T) {
	for _, test := range []struct {
		name      string
		peerDelta int
		rights    bool
		data      []byte
		good      bool
	}{
		{"exact", 0, false, []byte{'A', 'G', 'V', 1}, true},
		{"foreign-current-sender", 1, false, []byte{'A', 'G', 'V', 1}, false},
		{"extra-right", 0, true, []byte{'A', 'G', 'V', 1}, false},
		{"trailing-data", 0, false, []byte{'A', 'G', 'V', 1, 0}, false},
		{"other-phase", 0, false, []byte{'F', 'S', 'V', 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender, receiver := controllerPacketPairV1(t)
			source, err := os.Open("/dev/null")
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			var ancillary []byte
			if test.rights {
				ancillary = unix.UnixRights(int(source.Fd()))
			}
			if n, err := unix.SendmsgN(int(sender.Fd()), test.data, ancillary, nil, unix.MSG_NOSIGNAL); err != nil || n != len(test.data) {
				t.Fatal(n, err)
			}
			peer := gvisorControllerPeerV1(os.Getpid() + test.peerDelta)
			files, err := receiveGvisorControllerPacketV1(receiver, []byte{'A', 'G', 'V', 1}, 0, peer, false)
			closeFiles(files)
			if (err == nil) != test.good {
				t.Fatal("current SCM/descriptor/phase barrier changed", err)
			}
			if _, err := source.Stat(); err != nil {
				t.Fatal("refusal closed the sender's original object", err)
			}
		})
	}
}

func TestControllerEmptyPrivateQueueRefusesDataAndEOF(t *testing.T) {
	sender, receiver := controllerPacketPairV1(t)
	if err := checkGvisorControllerEmptyV1(receiver); err != nil {
		t.Fatal("fresh private queue was not empty", err)
	}
	if _, err := unix.SendmsgN(int(sender.Fd()), []byte{1}, nil, nil, unix.MSG_NOSIGNAL); err != nil {
		t.Fatal(err)
	}
	if err := checkGvisorControllerEmptyV1(receiver); err == nil {
		t.Fatal("queued data became private-release permission")
	}
	other, closed := controllerPacketPairV1(t)
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkGvisorControllerEmptyV1(closed); err == nil {
		t.Fatal("endpoint EOF became private-release permission")
	}
}
