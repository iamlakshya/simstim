//go:build linux

package microvm

import (
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestReadonlyDiskFramesCannotCreateNativePhaseFromMetadataV2(t *testing.T) {
	for _, kind := range []string{"mode_init_disk", "mode_init_disk_v2", "mode_disk_reply_v2"} {
		message, err := json.Marshal(runnerMessage{Kind: kind, ID: 1, Session: "fixed", Driver: "chv"})
		if err != nil {
			t.Fatal(err)
		}
		control := &runnerControl{pending: 1, gotReady: true}
		if err := control.acceptMessage(message, &proto.UnixControlV1{}); err == nil {
			t.Fatal("an unissued/old disk frame created a native source", kind)
		}
	}
}

// This exercises the actual server dispatch and native rights transport. It
// proves no original creation/source authority; that separate admission must
// still refuse a fixture frame from this ordinary test process.
func TestReadonlyServerReplyUsesActualNativeDiskDispatchV2(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	parent, err := owner.vm.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	outside, child, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outside.Close() })
	inside, err := net.FileConn(child)
	closeErr := child.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	t.Cleanup(func() { _ = inside.Close() })
	connection, ok := outside.(*net.UnixConn)
	if !ok {
		t.Fatal("the actual transport is not Unix")
	}
	if err := proto.EnableUnixCredentialsV1(connection); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := inside.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	server := newRunnerServer(inside)
	server.readonlyDisk = &readonlyControlDiskV2{root: owner.vm, disk: owner.disk, parent: parent, session: "fixed", driver: "chv", bytes: owner.bytes}
	sent := make(chan error, 1)
	go func() { sent <- server.send(runnerMessage{Kind: "reply", ID: 7, State: "Running"}) }()
	data := make([]byte, 4096)
	n, control, err := proto.ReceiveUnixControlV1(connection, data, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := control.CheckReceivedV1(connection); err != nil {
		t.Fatal(err)
	}
	var message runnerMessage
	if err := json.Unmarshal(data[:n], &message); err != nil {
		t.Fatal(err)
	}
	if message.Kind != "mode_disk_reply_v2" || message.ID != 7 || message.State != "Running" || message.Session != "fixed" || message.Driver != "chv" || len(control.Files) != 2 {
		t.Fatal("the actual server omitted its current native disk reply")
	}
	for index, original := range []*os.File{owner.disk, parent} {
		want, err := original.Stat()
		got, statErr := control.Files[index].Stat()
		if err != nil || statErr != nil || !os.SameFile(want, got) {
			t.Fatal("the actual native reply substituted its disk or parent", err, statErr)
		}
	}
	phase := &vmmExecPhaseV1{readonly: true, completed: true, command: &jail.Cmd{}, disk: owner.disk, diskParent: owner.diskParent}
	consumer := &runnerControl{mode: phase, gotReady: true, pending: 7}
	if err := consumer.acceptMessage(data[:n], &control); err == nil {
		t.Fatal("an ordinary transport fixture issued original native source authority")
	}
}

func TestReadonlyDiskLiteralPhaseCannotAuthorizeSnapshotBorrowV2(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	phase := &vmmExecPhaseV1{readonly: true, completed: true, command: &jail.Cmd{}, snapshotOwner: owner, disk: owner.disk, diskParent: owner.diskParent}
	// Negative only: neither these data fields nor genuine local descriptors
	// replace the original opaque source/atomic creation/native receive.
	owner.readonly = phase
	called := false
	if err := owner.borrow(t.Context(), func(*os.Root, *os.Root, *os.File) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatal("literal phase/real descriptor metadata issued a snapshot")
	}
	owner.readonly = nil // Restore the real fixture's local ownership for cleanup.
}

func TestReadonlyDiskReplyMustHaveSameOriginalPendingRequestV2(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	phase := &vmmExecPhaseV1{readonly: true, completed: true, command: &jail.Cmd{}, disk: owner.disk, diskParent: owner.diskParent}
	for _, id := range []uint64{0, 2} {
		control := &runnerControl{mode: phase, gotReady: true, pending: 1}
		message, err := json.Marshal(runnerMessage{Kind: "mode_disk_reply_v2", ID: id})
		if err != nil {
			t.Fatal(err)
		}
		if err := control.acceptMessage(message, &proto.UnixControlV1{}); err == nil {
			t.Fatal("another pending/unsolicited reply reached the actual native source")
		}
	}
	control := &runnerControl{mode: phase, gotReady: true, pending: 1}
	message, err := json.Marshal(runnerMessage{Kind: "reply", ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := control.acceptMessage(message, &proto.UnixControlV1{}); err == nil {
		t.Fatal("an original read-only request accepted a reply without native disk roles")
	}
}
