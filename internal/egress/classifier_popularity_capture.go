package egress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	maxPopularityBytesV1  = 64 << 20
	maxPopularityHeaderV1 = 16 << 10
	popularityArgumentV1  = "--popularity-fds-v1="
)

type popularityFileVersionV1 struct {
	Device, Inode uint64
	Size          int64
	Modified      int64
	Changed       int64
	Mode          uint32
}

type popularityBootstrapV1 struct {
	SessionID string                  `json:"session_id"`
	Version   int                     `json:"v"`
	Selected  string                  `json:"selected"`
	Original  popularityFileVersionV1 `json:"original"`
	Snapshot  popularityFileVersionV1 `json:"snapshot"`
	Digest    string                  `json:"digest"`
}

func popularityErrorV1() error {
	return &proto.Error{Code: "classifier_popularity", Message: "the configured popularity snapshot is unavailable", Hint: "Check the configured ranked-domain file on the host, then restart the sandbox."}
}

// PopularityPreparedV1 owns the pre-init native snapshot and its sole child
// handoff. Public metadata alone cannot construct the file it carries.
type PopularityPreparedV1 struct {
	mu         sync.Mutex
	file       *os.File
	header     []byte
	readEnd    *os.File
	writeEnd   *os.File
	done       chan struct{}
	stop       func() bool
	cancelDone chan struct{}
	used       bool
	closed     bool
}

func (*PopularityPreparedV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

// CapturePopularityV1 consumes one actual read-only regular file selected by
// the original trusted startup factory. It creates a sealed bounded snapshot
// before Egress starts. It accepts no path to open and exposes no file getter.
func CapturePopularityV1(ctx context.Context, original *os.File, selected string) (*PopularityPreparedV1, error) {
	if original == nil {
		return nil, popularityErrorV1()
	}
	defer original.Close()
	if ctx == nil || ctx.Err() != nil || !ValidPopularitySelectionV1(selected) {
		return nil, popularityErrorV1()
	}
	before, err := popularityOriginalVersionV1(original)
	if err != nil || before.Size < 0 || before.Size > maxPopularityBytesV1 {
		return nil, popularityErrorV1()
	}
	snapshot, err := newPopularitySnapshotV1()
	if err != nil {
		return nil, popularityErrorV1()
	}
	keep := false
	defer func() {
		if !keep {
			_ = snapshot.Close()
		}
	}()
	hash := sha256.New()
	reader := &popularityContextReaderV1{ctx: ctx, reader: io.NewSectionReader(original, 0, maxPopularityBytesV1+1)}
	count, err := io.CopyBuffer(io.MultiWriter(snapshot, hash), reader, make([]byte, 32<<10))
	after, afterErr := popularityOriginalVersionV1(original)
	if err != nil || afterErr != nil || before != after || count != before.Size || count > maxPopularityBytesV1 || ctx.Err() != nil {
		return nil, popularityErrorV1()
	}
	sealed, err := sealPopularitySnapshotV1(snapshot)
	if err != nil {
		return nil, popularityErrorV1()
	}
	_ = snapshot.Close()
	snapshot = sealed
	version, err := popularitySnapshotVersionV1(snapshot)
	if err != nil || version.Size != count {
		return nil, popularityErrorV1()
	}
	meta := popularityBootstrapV1{Version: 1, Selected: selected, Original: before, Snapshot: version, Digest: hex.EncodeToString(hash.Sum(nil))}
	header, err := json.Marshal(meta)
	if err != nil || len(header) > maxPopularityHeaderV1 {
		return nil, popularityErrorV1()
	}
	keep = true
	return &PopularityPreparedV1{file: snapshot, header: header}, nil
}

type popularityContextReaderV1 struct {
	ctx    context.Context
	reader io.Reader
}

func (r *popularityContextReaderV1) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

// AppendEgressCommandV1 gives this snapshot only to the original internal
// Egress command. Its bounded private pipe binds the original selection and
// native snapshot before control init; no filename goes on the command line.
func (p *PopularityPreparedV1) AppendEgressCommandV1(ctx context.Context, cmd *exec.Cmd, id string) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || p.used || p.closed || p.file == nil || !api.ValidSessionID(id) || cmd == nil || len(cmd.Args) != 3 || cmd.Args[1] != "egress" || cmd.Args[2] != id || len(cmd.ExtraFiles) < 1 || cmd.ExtraFiles[0] == nil {
		return popularityErrorV1()
	}
	base := 3 + len(cmd.ExtraFiles)
	if base < 4 || base > 64 {
		return popularityErrorV1()
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return popularityErrorV1()
	}
	var meta popularityBootstrapV1
	if json.Unmarshal(p.header, &meta) != nil {
		reader.Close()
		writer.Close()
		return popularityErrorV1()
	}
	meta.SessionID = id
	header, err := json.Marshal(meta)
	if err != nil || len(header) > maxPopularityHeaderV1 {
		reader.Close()
		writer.Close()
		return popularityErrorV1()
	}
	p.readEnd, p.writeEnd, p.done, p.used = reader, writer, make(chan struct{}), true
	p.cancelDone = make(chan struct{})
	cancelled := p.cancelDone
	p.stop = context.AfterFunc(ctx, func() { _ = writer.Close(); close(cancelled) })
	cmd.ExtraFiles = append(cmd.ExtraFiles, p.file, reader)
	cmd.Args = append(cmd.Args, popularityArgumentV1+strconv.Itoa(base))
	go func() {
		defer close(p.done)
		defer writer.Close()
		_, _ = writer.Write(header)
	}()
	return nil
}

// Close joins the original bounded handoff and releases only parent copies.
func (p *PopularityPreparedV1) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	file, reader, writer, done, stop, cancelled := p.file, p.readEnd, p.writeEnd, p.done, p.stop, p.cancelDone
	p.mu.Unlock()
	waitCancelled := false
	if stop != nil {
		waitCancelled = !stop()
	}
	if writer != nil {
		_ = writer.Close()
	}
	if reader != nil {
		_ = reader.Close()
	}
	if done != nil {
		<-done
	}
	if waitCancelled {
		<-cancelled
	}
	if file != nil {
		return file.Close()
	}
	return nil
}

// ParsePopularityFDArgumentV1 validates only the internal descriptor layout.
// The actual inherited sealed file and private bootstrap remain mandatory.
func ParsePopularityFDArgumentV1(value string) (int, bool) {
	if !strings.HasPrefix(value, popularityArgumentV1) {
		return 0, false
	}
	raw := strings.TrimPrefix(value, popularityArgumentV1)
	base, err := strconv.Atoi(raw)
	return base, err == nil && base >= 4 && base <= 64 && strconv.Itoa(base) == raw
}
