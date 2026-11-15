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
	conn      net.Conn
	readMu    sync.Mutex
	writeMu   sync.Mutex
	mu        sync.Mutex
	closed    bool
	candidate *pendingStartupV1
	reading   bool
	bytes     int64
	start     int64
	files     []*os.File
	cred      *startupCredentials
	controlAt int64
}

// NewStartupLineV1 must run before any buffered read of this connection.
func NewStartupLineV1(conn net.Conn) (*StartupLineV1, error) {
	if conn == nil {
		return nil, sessionLeaseError("startup has no original control connection")
	}
	if err := enableStartupAncillary(conn); err != nil {
		return nil, err
	}
	return &StartupLineV1{conn: conn}, nil
}

// ReadFrame decodes one ordinary protocol frame with no read-ahead across its
// end. The next read refuses an unclaimed descriptor rather than losing it.
func (line *StartupLineV1) ReadFrame() (proto.Frame, error) {
	line.readMu.Lock()
	defer line.readMu.Unlock()
	line.mu.Lock()
	if line.closed || len(line.files) != 0 {
		err := line.closeAncillaryLocked()
		line.mu.Unlock()
		return proto.Frame{}, errors.Join(sessionLeaseError("startup has unclaimed native control data"), err)
	}
	line.start, line.reading = line.bytes, true
	line.cred = nil
	line.mu.Unlock()
	frame, err := proto.ReadFrom(startupFrameInput{line})
	line.mu.Lock()
	line.reading = false
	if err != nil {
		err = errors.Join(err, line.closeAncillaryLocked())
	}
	line.mu.Unlock()
	return frame, err
}

type startupFrameInput struct{ line *StartupLineV1 }

func (input startupFrameInput) Read(data []byte) (int, error) {
	line := input.line
	n, files, cred, err := receiveStartupAncillary(line.conn, data)
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || !line.reading {
		err = errors.Join(err, sessionLeaseError("the startup reader ended"))
	}
	if cred != nil {
		if line.cred != nil && *line.cred != *cred {
			err = errors.Join(err, sessionLeaseError("a startup frame has more than one actual native sender"))
		}
		line.cred = cred
	}
	if len(files) != 0 {
		if len(line.files) != 0 || line.bytes != line.start || len(files) != 1 || cred == nil {
			err = errors.Join(err, sessionLeaseError("the startup descriptor is not attached to one frame boundary"))
		}
		line.controlAt = line.bytes
		line.files = append(line.files, files...)
	}
	line.bytes += int64(n)
	if err != nil {
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
	line.files, line.cred = nil, nil
	return file, cred, nil
}

func (line *StartupLineV1) closeAncillaryLocked() error {
	var failure error
	for _, file := range line.files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
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
		return nil
	}
	line.closed = true
	failure := errors.Join(line.conn.Close(), line.closeAncillaryLocked())
	if line.candidate != nil {
		failure = errors.Join(failure, line.candidate.peer.Close())
	}
	return failure
}

// SetDeadline bounds startup control without replacing the physical reader.
func (line *StartupLineV1) SetDeadline(deadline time.Time) error {
	return line.conn.SetDeadline(deadline)
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
	n, files, _, failure := receiveStartupAncillary(input.line.conn, data)
	for _, file := range files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	if len(files) != 0 {
		failure = errors.Join(failure, sessionLeaseError("unexpected native descriptors after startup"))
	}
	return n, failure
}
