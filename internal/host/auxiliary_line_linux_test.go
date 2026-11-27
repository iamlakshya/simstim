//go:build linux

package host

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

// These exercise only the real native carrier. The received ordinary fixture
// descriptors are not OP/holder capabilities and never enter a boot issuer.
func TestAuxiliaryLineCarriesExactlyThreeNativeRolesAndOrdinaryRejects(t *testing.T) {
	for _, auxiliary := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "auxiliary"}[auxiliary], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			accepted, err := listener.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer accepted.Close()
			sender, err := newAuxiliaryStartupLineV1(connection)
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			var receiver *StartupLineV1
			if auxiliary {
				receiver, err = newAuxiliaryStartupLineV1(accepted)
			} else {
				receiver, err = NewStartupLineV1(accepted)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			first, err := os.CreateTemp(t.TempDir(), "first")
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			second, err := os.CreateTemp(t.TempDir(), "second")
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			sent := make(chan error, 1)
			go func() {
				sent <- sender.writeAuxiliaryLoanV1(proto.Request{ID: 1, Op: auxiliaryLoanOperationV1}, first, second, first)
			}()
			frame, readErr := receiver.ReadFrame()
			if sendErr := <-sent; sendErr != nil {
				t.Fatal(sendErr)
			}
			if !auxiliary {
				if readErr == nil {
					t.Fatal("ordinary startup accepted three native roles")
				}
				return
			}
			if readErr != nil || frame.Type != proto.TRequest {
				t.Fatalf("native vector frame: %v", readErr)
			}
			roles, _, err := receiver.takeAuxiliaryFilesV1()
			if err != nil {
				t.Fatal(err)
			}
			if len(roles) != 3 {
				t.Fatal("native vector count changed")
			}
			defer roles[0].Close()
			defer roles[1].Close()
			defer roles[2].Close()
			defer roles[2].Close()
			for index, want := range []*os.File{first, second, first} {
				expected, err := want.Stat()
				if err != nil {
					t.Fatal(err)
				}
				actual, err := roles[index].Stat()
				if err != nil || !os.SameFile(expected, actual) {
					t.Fatalf("native role %d changed: %v", index, err)
				}
			}
			if _, _, err := receiver.takeAuxiliaryFilesV1(); err == nil {
				t.Fatal("one native vector was consumed twice")
			}
		})
	}
}
func TestAuxiliarySourceCannotBeIssuedByDataOnlyOwners(t *testing.T) {
	for _, owner := range []*auxiliaryOperationV1{nil, {}, {operation: &SessionOperation{id: "primary2"}}} {
		if err := owner.startAuxiliaryNativeV1(context.Background()); err == nil {
			t.Fatal("data-only owner dispatched native work")
		}
	}
	for _, boot := range []*AuxiliaryBootV1{nil, {}, {manifest: Manifest{ID: "primary2"}}} {
		if err := boot.Check(context.Background()); err == nil {
			t.Fatal("data-only source accepted boot")
		}
	}
}
