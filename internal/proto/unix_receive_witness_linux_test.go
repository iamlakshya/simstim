//go:build linux

package proto

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func witnessPairV1(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ends [2]*net.UnixConn
	for i, fd := range fds {
		file := os.NewFile(uintptr(fd), "native-receive-fixture")
		conn, err := net.FileConn(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		ends[i] = conn.(*net.UnixConn)
		t.Cleanup(func() { conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return ends[0], ends[1]
}

func receiveWitnessFixtureV1(t *testing.T) (*net.UnixConn, *UnixControlV1) {
	t.Helper()
	receiver, sender := witnessPairV1(t)
	if err := EnableUnixCredentialsV1(receiver); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "native-source")
	if err := os.WriteFile(path, []byte("actual owned source"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := SendUnixControlV1(sender, []byte{'x'}, []*os.File{file}); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	n, control, err := ReceiveUnixControlV1(receiver, one[:], 1)
	if err != nil || n != 1 || one[0] != 'x' || len(control.Files) != 1 || control.Credentials == nil {
		t.Fatal(n, err)
	}
	t.Cleanup(func() { control.Close() })
	return receiver, &control
}

func TestNativeReceiveWitnessRejectsReconstructionMutationAndWrongConnection(t *testing.T) {
	for _, change := range []string{"reconstruct", "credentials", "files", "connection", "closed"} {
		t.Run(change, func(t *testing.T) {
			conn, control := receiveWitnessFixtureV1(t)
			if err := control.CheckReceivedV1(conn); err != nil {
				t.Fatal(err)
			}
			original := control.Files[0]
			candidate := control
			switch change {
			case "reconstruct":
				copyCred := *control.Credentials
				candidate = &UnixControlV1{Files: append([]*os.File(nil), control.Files...), Credentials: &copyCred}
			case "credentials":
				control.Credentials.PID++
			case "files":
				foreign, err := os.CreateTemp(t.TempDir(), "unclaimed")
				if err != nil {
					t.Fatal(err)
				}
				defer foreign.Close()
				control.Files[0] = foreign
				if err := control.CheckReceivedV1(conn); err == nil {
					t.Fatal("changed native vector was blessed")
				}
				if err := control.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := original.Stat(); err == nil {
					t.Fatal("mutated fields leaked the actual received right")
				}
				if _, err := foreign.Stat(); err != nil {
					t.Fatal("cleanup closed an unclaimed foreign file", err)
				}
				return
			case "connection":
				conn, _ = witnessPairV1(t)
			case "closed":
				conn.Close()
			}
			if err := candidate.CheckReceivedV1(conn); err == nil {
				t.Fatal("invalid native provenance was accepted", change)
			}
		})
	}
}

func TestNativeReceiveWitnessCopiesShareOneActualTransferAndClose(t *testing.T) {
	conn, control := receiveWitnessFixtureV1(t)
	copy := *control
	files, err := control.DetachFilesV1()
	if err != nil || len(files) != 1 {
		t.Fatal(err)
	}
	defer files[0].Close()
	if err := copy.CheckReceivedV1(conn); err == nil {
		t.Fatal("consumed native receipt was replayed")
	}
	if _, err := copy.DetachFilesV1(); err == nil {
		t.Fatal("the same materialised vector was transferred twice")
	}
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := files[0].Stat(); err != nil {
		t.Fatal("closing a consumed receipt revoked its actual new owner", err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeReceiveCloseInvalidatesEveryCopyAndLegacyDoesNotMintWitness(t *testing.T) {
	conn, control := receiveWitnessFixtureV1(t)
	copy := *control
	original := control.Files[0]
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copy.CheckReceivedV1(conn); err == nil {
		t.Fatal("closed actual receipt remained live")
	}
	if _, err := original.Stat(); err == nil {
		t.Fatal("original received right was not closed")
	}
	if err := copy.Close(); err != nil {
		t.Fatal("copied receipt closed the same right twice", err)
	}
	file, err := os.CreateTemp(t.TempDir(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	legacy := UnixControlV1{Files: []*os.File{file}}
	if err := legacy.CheckReceivedV1(conn); err == nil {
		t.Fatal("hand-built fixture acquired native provenance")
	}
	files, err := legacy.DetachFilesV1()
	if err != nil || len(files) != 1 || files[0] != file {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("ordinary fixture ownership transfer changed", err)
	}
}
