//go:build linux

package guest

import (
	"encoding/binary"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func buildNLA(nlaType uint16, payload []byte) []byte {
	nlaLen := 4 + len(payload)
	padded := (nlaLen + 3) &^ 3
	buf := make([]byte, padded)
	binary.LittleEndian.PutUint16(buf[0:2], uint16(nlaLen))
	binary.LittleEndian.PutUint16(buf[2:4], nlaType)
	copy(buf[4:], payload)
	return buf
}

func TestFindMcastGroupID(t *testing.T) {
	idBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(idBytes, 42)
	entry1 := append(buildNLA(ctrlAttrMcastName, []byte("other_group\x00")), buildNLA(ctrlAttrMcastID, []byte{7, 0, 0, 0})...)
	entry2 := append(buildNLA(ctrlAttrMcastName, []byte("acpi_mc_group\x00")), buildNLA(ctrlAttrMcastID, idBytes)...)
	list := append(buildNLA(1, entry1), buildNLA(2, entry2)...)

	got, ok := findMcastGroupID(list, "acpi_mc_group")
	if !ok || got != 42 {
		t.Fatalf("findMcastGroupID(acpi_mc_group) = (%d, %v), want (42, true)", got, ok)
	}
	if _, ok := findMcastGroupID(list, "missing_group"); ok {
		t.Fatal("expected missing_group not to match")
	}
	if _, ok := findMcastGroupID([]byte{1, 0, 0}, "acpi_mc_group"); ok {
		t.Fatal("expected truncated data not to match")
	}
}

func TestShouldRunMicroVMDisambiguation(t *testing.T) {
	cases := []struct {
		name         string
		pid          int
		arg0         string
		args         []string
		linkIsSocket bool
		wantHandled  bool
		wantSelfTest bool
	}{
		{
			name:         "self-test flag always enters microvm self-test",
			pid:          42,
			arg0:         "/usr/bin/helper",
			args:         []string{"--self-test"},
			linkIsSocket: false,
			wantHandled:  true,
			wantSelfTest: true,
		},
		{
			name:         "microvm PID 1 /init with no link socket enters microvm",
			pid:          1,
			arg0:         "/init",
			args:         nil,
			linkIsSocket: false,
			wantHandled:  true,
			wantSelfTest: false,
		},
		{
			name:         "gVisor or bwrap PID 1 with inherited link socket stays on linkFD",
			pid:          1,
			arg0:         "/run/helper/host",
			args:         nil,
			linkIsSocket: true,
			wantHandled:  false,
			wantSelfTest: false,
		},
		{
			name:         "PID 1 named /init with inherited link socket still stays on linkFD",
			pid:          1,
			arg0:         "/init",
			args:         nil,
			linkIsSocket: true,
			wantHandled:  false,
			wantSelfTest: false,
		},
		{
			name:         "PID 1 with non-init argv0 and no socket does not hijack into microvm",
			pid:          1,
			arg0:         "/run/helper/host",
			args:         nil,
			linkIsSocket: false,
			wantHandled:  false,
			wantSelfTest: false,
		},
		{
			name:         "non-PID-1 /init does not enter microvm",
			pid:          2,
			arg0:         "/init",
			args:         nil,
			linkIsSocket: false,
			wantHandled:  false,
			wantSelfTest: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handled, selfTest := shouldRunMicroVM(tc.pid, tc.arg0, tc.args, tc.linkIsSocket)
			if handled != tc.wantHandled || selfTest != tc.wantSelfTest {
				t.Fatalf("shouldRunMicroVM(%d, %q, %v, %v) = (%v, %v), want (%v, %v)",
					tc.pid, tc.arg0, tc.args, tc.linkIsSocket, handled, selfTest, tc.wantHandled, tc.wantSelfTest)
			}
		})
	}
}

func TestIsSocketFD(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("Socketpair: %v", err)
	}
	defer func() {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	}()
	if !isSocketFD(fds[0]) {
		t.Errorf("isSocketFD(socketpair) = false, want true")
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	if isSocketFD(int(pr.Fd())) {
		t.Errorf("isSocketFD(pipe) = true, want false")
	}
}
