//go:build linux

package microvm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Constructed only by runReadonlyVMControlV1 after its actual original native
// captured entry, inherited-parent match and own UID1 anchored disk open.
// It supplies current named identity, never a launch/exit/publication grant.
type readonlyControlDiskV2 struct {
	root            *os.Root
	disk, parent    *os.File
	session, driver string
	bytes           int64
	checkpoint      *checkpointActorStoreV3
}

// The existing server mutex serializes this actual reply with raw SCM frames.
func (server *runnerServer) sendReadonlyDiskReplyV2(message runnerMessage) error {
	owner := server.readonlyDisk
	if owner == nil || message.ID == 0 || owner.root == nil || owner.disk == nil || owner.parent == nil {
		return fmt.Errorf("the original read-only disk reply has no actual owner/request")
	}
	info, err := cachefs.ValidateFile(owner.root, SessionDiskName, owner.disk, 0o600)
	if err != nil || info.Size() != owner.bytes {
		return errors.Join(err, fmt.Errorf("the current subordinate disk changed its original anchored identity/size"))
	}
	actual, err := owner.root.Stat(".")
	parent, parentErr := owner.parent.Stat()
	if err != nil || parentErr != nil || !os.SameFile(actual, parent) {
		return errors.Join(err, parentErr, fmt.Errorf("the current subordinate disk parent changed identity"))
	}
	connection, ok := server.conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("the original read-only disk reply lost its native stream")
	}
	message.Kind, message.Session, message.Driver = "mode_disk_reply_v2", owner.session, owner.driver
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return proto.SendUnixControlV1(connection, append(data, '\n'), []*os.File{owner.disk, owner.parent})
}

func (phase *vmmExecPhaseV1) acceptReadonlyReplyV2(message runnerMessage, control *proto.UnixControlV1) error {
	phase.mu.Lock()
	defer phase.mu.Unlock()
	if !phase.readonly || !phase.completed || phase.disk == nil || phase.diskParent == nil || message.ID == 0 || message.Session != phase.session || message.Driver != phase.driver {
		return fmt.Errorf("the native disk reply has another original read-only phase")
	}
	return phase.command.CheckVMMReadonlyDiskReplyV2(phase.source, phase.control, control)
}
