package host

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

type startupCredentials struct {
	pid int
	uid uint32
	gid uint32
}

// StartupLineV1 owns the sole physical reader from the first hello byte.
// Native descriptors stay attached to exactly one decoded frame. A discarded
// frame, read failure or close also closes every materialised descriptor.
// This transport supplies no startup or execution authority by itself.
type StartupLineV1 struct {
	nativeRetirement    *nativeDescriptorRetirementsV1
	receiveRetention    *proto.UnixControlRetentionV1
	nativeReadStarted   bool
	prepareCursor       *sandboxPrepareFrameCursorV1
	preparePoison       bool
	nativePeer          *nativeStartupPeerV1
	loanFiles           int // private closed-purpose role count; ordinary startup remains one
	conn                net.Conn
	readMu              sync.Mutex
	writeMu             sync.Mutex
	mu                  sync.Mutex
	closed              bool
	closeErr            error
	candidate           *pendingStartupV1
	reading             bool
	bytes               int64
	start               int64
	files               []*os.File
	cred                *startupCredentials
	controlAt           int64
	slotStartupPending  *slotReservedStartupPendingV2 // exact new-creation startup19
	snapshotLoan        bool                          // armed only by the original warm Snapshot receiver
	snapshotReservation bool                          // separate exact 18-role reserved Snapshot class
}

// NewStartupLineV1 must run before any buffered read of this connection.
func NewStartupLineV1(conn net.Conn) (*StartupLineV1, error) {
	if conn == nil {
		return nil, sessionLeaseError("startup has no original control connection")
	}
	if err := enableStartupAncillary(conn); err != nil {
		return nil, err
	}
	return &StartupLineV1{conn: conn, loanFiles: 1}, nil
}

// ReadFrame decodes one ordinary protocol frame with no read-ahead across its
// end. The next read refuses an unclaimed descriptor rather than losing it.
func (line *StartupLineV1) ReadFrame() (proto.Frame, error) {
	line.readMu.Lock()
	defer line.readMu.Unlock()
	line.mu.Lock()
	line.nativeReadStarted = true
	resuming := line.prepareCursor != nil && line.prepareCursor.active
	if line.closed || line.preparePoison || len(line.files) != 0 && !resuming {
		err := line.closeAncillaryLocked()
		line.mu.Unlock()
		return proto.Frame{}, errors.Join(sessionLeaseError("startup has unclaimed native control data"), err)
	}
	if !resuming {
		line.start = line.bytes
		line.cred = nil
	}
	line.reading = true
	cursor := line.prepareCursor
	if cursor != nil {
		cursor.active = true
	}
	line.mu.Unlock()
	var frame proto.Frame
	var err error
	if cursor != nil {
		frame, err = cursor.read(startupFrameInput{line})
	} else {
		frame, err = proto.ReadFrom(startupFrameInput{line})
	}
	line.mu.Lock()
	line.reading = false
	if cursor != nil && line.preparePoison && err == nil {
		err = sessionLeaseError("the original preparation frame lost its native control integrity")
		frame = proto.Frame{}
	}
	if err != nil && (cursor == nil || line.preparePoison || !sandboxPrepareReadDeadlineV1(err)) {
		line.preparePoison = cursor != nil
		err = errors.Join(err, line.closeAncillaryLocked())
	}
	line.mu.Unlock()
	return frame, err
}

type startupFrameInput struct{ line *StartupLineV1 }

func (input startupFrameInput) Read(data []byte) (int, error) {
	line := input.line
	line.mu.Lock()
	snapshot := line.snapshotLoan
	reservation := line.snapshotReservation
	startupReservation := line.slotStartupPending != nil
	retention := line.receiveRetention
	line.nativeReadStarted = true
	line.mu.Unlock()
	if (snapshot && reservation) || (startupReservation && (snapshot || reservation)) {
		return 0, sessionLeaseError("the native reader has incompatible closed role classes")
	}
	var n int
	var files []*os.File
	var cred *startupCredentials
	var err error
	if retention != nil {
		if startupReservation {
			n, files, cred, err = receiveSlotReservedStartupRetainedV4(line.conn, data, retention)
		} else if reservation {
			n, files, cred, err = receiveSlotSnapshotReservationRetainedV4(line.conn, data, retention)
		} else if snapshot {
			n, files, cred, err = receiveSlotSnapshotRetainedV4(line.conn, data, retention)
		} else {
			n, files, cred, err = receiveStartupAncillaryRetainedBoundV4(line.conn, data, line.loanFiles, retention)
		}
	} else if startupReservation {
		n, files, cred, err = receiveSlotReservedStartupAncillaryV2(line.conn, data)
	} else if reservation {
		n, files, cred, err = receiveSlotSnapshotReservationAncillaryV2(line.conn, data)
	} else if snapshot {
		n, files, cred, err = receiveSlotSnapshotAncillaryV1(line.conn, data)
	} else {
		n, files, cred, err = receiveStartupAncillaryBoundV1(line.conn, data, line.loanFiles)
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	fatal := false
	if line.closed || !line.reading {
		fatal = true
		err = errors.Join(err, sessionLeaseError("the startup reader ended"))
	}
	if cred != nil {
		if line.cred != nil && *line.cred != *cred {
			fatal = true
			err = errors.Join(err, sessionLeaseError("a startup frame has more than one actual native sender"))
		}
		line.cred = cred
	}
	if len(files) != 0 {
		bound := line.loanFiles
		if snapshot {
			bound = proto.NativeCheckpointRoleCountV1
		}
		if reservation {
			bound = proto.NativeCheckpointReservationRoleCountV2
		}
		if startupReservation {
			bound = proto.NativeSlotReservedStartupRoleCountV2
		}
		if len(line.files) != 0 || line.bytes != line.start || len(files) > bound || cred == nil {
			fatal = true
			err = errors.Join(err, sessionLeaseError("the startup descriptor is not attached to one frame boundary"))
		}
		line.controlAt = line.bytes
		line.files = append(line.files, files...)
	}
	line.bytes += int64(n)
	if fatal && line.prepareCursor != nil {
		line.preparePoison = true
	}
	if err != nil && (line.prepareCursor == nil || fatal || !sandboxPrepareReadDeadlineV1(err)) {
		err = errors.Join(err, line.closeAncillaryLocked())
	}
	if n == 0 && err == nil {
		err = io.EOF
	}
	return n, err
}

// RejectAncillary ensures ordinary hello, request and running frames carry no
// descriptors. Native startup consumers claim a loan before the next read.
func (line *StartupLineV1) RejectAncillary() error {
	line.mu.Lock()
	defer line.mu.Unlock()
	if len(line.files) == 0 {
		return nil
	}
	return errors.Join(sessionLeaseError("unexpected native startup descriptors"), line.closeAncillaryLocked())
}

func (line *StartupLineV1) takeLoanFile() (*os.File, startupCredentials, error) {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || len(line.files) != 1 || line.cred == nil || line.controlAt != line.start {
		return nil, startupCredentials{}, errors.Join(sessionLeaseError("startup has no single current native loan"), line.closeAncillaryLocked())
	}
	file, cred := line.files[0], *line.cred
	line.files = nil // Current credentials still belong to this decoded frame.
	return file, cred, nil
}

func (line *StartupLineV1) closeAncillaryLocked() error {
	failure := closeStartupReceivedFilesV4(line.nativeRetirement, line.files)
	line.files, line.cred = nil, nil
	return failure
}

// WriteFrame serialises ordinary frames with the loan on this same connection.
func (line *StartupLineV1) WriteFrame(kind proto.Type, value any) error {
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	return proto.NewWriter(line.conn).WriteJSON(kind, value)
}

func (line *StartupLineV1) writeLoan(value any, file *os.File) error {
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return sendStartupAncillary(line.conn, proto.TRequest, raw, file)
}

// Close ends the physical connection and closes all unclaimed native data.
func (line *StartupLineV1) Close() error {
	if line == nil {
		return nil
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed {
		return line.closeErr
	}
	line.closed = true
	failure := errors.Join(line.conn.Close(), line.closeAncillaryLocked())
	if line.nativePeer != nil {
		failure = errors.Join(failure, line.nativePeer.Close())
	}
	if line.candidate != nil {
		failure = errors.Join(failure, line.candidate.peer.Close())
	}
	line.closeErr = failure
	return line.closeErr
}

// SetDeadline bounds startup control without replacing the physical reader.
func (line *StartupLineV1) SetDeadline(deadline time.Time) error {
	return line.conn.SetDeadline(deadline)
}

func (line *StartupLineV1) isClosed() bool {
	if line == nil {
		return true
	}
	line.mu.Lock()
	defer line.mu.Unlock()
	return line.closed
}

func (line *StartupLineV1) currentSender() (startupCredentials, error) {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || line.cred == nil {
		return startupCredentials{}, sessionLeaseError("the startup frame has no current native sender")
	}
	return *line.cred, nil
}

// OrdinaryReader continues the same physical reader after startup. It refuses
// and closes every later descriptor; it must replace, rather than accompany,
// the startup ReadFrame loop when the existing running dispatcher takes over.
func (line *StartupLineV1) OrdinaryReader() io.Reader { return startupOrdinaryInput{line} }

type startupOrdinaryInput struct{ line *StartupLineV1 }

func (input startupOrdinaryInput) Read(data []byte) (int, error) {
	input.line.readMu.Lock()
	defer input.line.readMu.Unlock()
	line := input.line
	line.mu.Lock()
	line.nativeReadStarted = true
	retention, retirement := line.receiveRetention, line.nativeRetirement
	line.mu.Unlock()
	var n int
	var files []*os.File
	var failure error
	if retention != nil {
		n, files, _, failure = receiveStartupAncillaryRetainedBoundV4(line.conn, data, 1, retention)
	} else {
		n, files, _, failure = receiveStartupAncillary(line.conn, data)
	}
	failure = errors.Join(failure, closeStartupReceivedFilesV4(retirement, files))
	if len(files) != 0 {
		failure = errors.Join(failure, sessionLeaseError("unexpected native descriptors after startup"))
	}
	return n, failure
}
