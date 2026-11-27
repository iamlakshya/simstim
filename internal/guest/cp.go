package guest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const CopyHelperMode = "wall-cp-v1"
const copyMetadataMax = 1 << 20

func copyError(err error) *proto.Error {
	var core *filecopy.Error
	if errors.As(err, &core) {
		return &proto.Error{Code: core.Code, Message: core.Message, Hint: core.Hint}
	}
	return &proto.Error{Code: "cp_path", Message: "the sandbox cannot read the selected ordinary path", Hint: "Copy a readable file or folder beneath /work, /home/agent, /tmp or /mnt."}
}

// RunCopyHelper is one closed fixed-account list/read operation. It accepts
// only the SAME versioned get header, no command, UID or ambient host root.
func RunCopyHelper(args []string) (returned error) {
	if len(args) != 0 {
		return copyError(errors.New("copy helper arguments"))
	}
	if err := checkInputHelperAccount(); err != nil {
		return err
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := proto.ReadJSONLine(reader)
	if err != nil || len(line) > link.DefaultMaxHeader {
		return copyError(err)
	}
	var header proto.FileGetHeaderV1
	if !api.ExactJSONFields(line, header) || json.Unmarshal(line, &header) != nil {
		return copyError(errors.New("copy fields"))
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		return copyError(errors.New("trailing copy control"))
	}
	data, err := readCopyData(context.Background(), header)
	result := proto.FileGetResultV1{Version: proto.FileTransferVersionV1, Size: int64(len(data))}
	if err != nil {
		result.Size = 0
		result.Error = copyError(err)
		data = nil
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func readCopyData(ctx context.Context, header proto.FileGetHeaderV1) (_ []byte, returned error) {
	if header.Version != proto.FileTransferVersionV1 || header.Offset < 0 || header.List && (header.Offset != 0 || header.MaxBytes != 0) || !header.List && (header.MaxBytes <= 0 || header.MaxBytes > proto.FileTransferChunkMaxV1) {
		return nil, copyError(errors.New("copy bounds"))
	}
	top, relative, err := filecopy.GuestPath(header.Path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	if header.List {
		entries, isDirectory, err := filecopy.List(ctx, root, relative, 10000)
		if err != nil {
			return nil, err
		}
		result := proto.CopyListResultV1{Version: proto.FileTransferVersionV1, Name: path.Base(path.Join(top, relative)), Directory: isDirectory, Entries: []proto.CopyListEntryV1{}}
		for _, entry := range entries {
			result.Entries = append(result.Entries, proto.CopyListEntryV1{Path: entry.Path, Directory: entry.Kind == "directory", Size: entry.Size, Mode: entry.Kind})
		}
		data, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		if len(data) > copyMetadataMax {
			return nil, &filecopy.Error{Code: "cp_too_big", Message: "the copy listing exceeds 1 MiB", Hint: "Copy a smaller folder with at most 10000 entries."}
		}
		return data, nil
	}
	var data bytes.Buffer
	if _, err := filecopy.WriteChunk(ctx, root, relative, header.Offset, header.MaxBytes, &data); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

// acceptCopy shares actual admission/shutdown with the existing put receiver.
func (g *Init) acceptCopy(st *link.Stream) {
	g.mu.Lock()
	if g.ended || !g.sessionPrepared || g.lifetime == nil {
		g.mu.Unlock()
		_ = st.Close()
		return
	}
	g.fileWG.Add(1)
	g.mu.Unlock()
	go func() { defer g.fileWG.Done(); g.serveCopy(st) }()
}

func (g *Init) serveCopy(st *link.Stream) {
	defer st.Close()
	ctx, cancel := context.WithTimeout(g.lifetime, 2*time.Minute)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = st.SetDeadline(time.Now()); _ = st.Close() })
	defer stop()
	var header proto.FileGetHeaderV1
	if st.Header(&header) != nil || !api.ExactJSONFields(st.HeaderBytes(), struct {
		Kind link.Kind `json:"kind"`
		proto.FileGetHeaderV1
	}{}) {
		return
	}
	if header.Version != proto.FileTransferVersionV1 || header.List && (header.Offset != 0 || header.MaxBytes != 0) || !header.List && (header.Offset < 0 || header.MaxBytes <= 0 || header.MaxBytes > proto.FileTransferChunkMaxV1) {
		g.copyReply(st, nil, copyError(errors.New("copy bounds")))
		return
	}
	if _, _, err := filecopy.GuestPath(header.Path); err != nil {
		g.copyReply(st, nil, err)
		return
	}
	executable, err := g.helperExecutable()
	if err != nil {
		g.copyReply(st, nil, err)
		return
	}
	prefix, err := json.Marshal(header)
	if err != nil {
		g.copyReply(st, nil, err)
		return
	}
	run := newOwnedCommand(ctx, proto.CtlExecArgs{Argv: []string{executable, CopyHelperMode}, Cwd: "/"}, inputCommandEnv())
	run.vmRole = vmSeedCommand
	defer run.cancel()
	// A valid header is followed by exact input EOF. The ordered half-close
	// proves no body; a reset/full-close is never a successful empty handoff.
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	var unexpected [1]byte
	n, endErr := st.Read(unexpected[:])
	if n != 0 || !errors.Is(endErr, io.EOF) || !st.PeerWriteClosed() {
		g.copyReply(st, nil, copyError(errors.New("copy stream carries a body or lost its handoff")))
		return
	}
	_ = st.SetReadDeadline(time.Time{})
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		_ = st.WaitPeerGone(watchCtx)
		if watchCtx.Err() == nil {
			cancel()
		}
	}()
	defer func() { stopWatch(); <-watchDone }()
	// Queue rather than refuse accepted streams. At most two helpers buffer a
	// bounded reply in trusted Init at once; the link's peer counts stay intact.
	g.mu.Lock()
	if g.copySlots == nil {
		g.copySlots = make(chan struct{}, 2)
	}
	slots := g.copySlots
	g.mu.Unlock()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return
	}
	var output copyOutput
	code := g.executeOwned(run, ownedCommandIO{Input: bytes.NewReader(append(prefix, '\n')), Out: &output, Err: io.Discard, AbortOutput: func() { _ = st.SetDeadline(time.Now()) }})
	stopWatch()
	<-watchDone
	if code != 0 || ctx.Err() != nil || output.failed {
		g.copyReply(st, nil, copyError(errors.New("owned copy failed")))
		return
	}
	reader := bufio.NewReader(bytes.NewReader(output.Bytes()))
	line, err := proto.ReadJSONLine(reader)
	var result proto.FileGetResultV1
	if err != nil || len(line) > link.DefaultMaxHeader || !api.ExactJSONFields(line, result) || json.Unmarshal(line, &result) != nil || result.Version != proto.FileTransferVersionV1 || result.Size < 0 || result.Size > copyMetadataMax {
		g.copyReply(st, nil, copyError(errors.New("invalid helper copy result")))
		return
	}
	data, err := io.ReadAll(io.LimitReader(reader, result.Size+1))
	if err != nil || int64(len(data)) != result.Size || result.Error != nil && result.Size != 0 {
		g.copyReply(st, nil, copyError(errors.New("invalid helper copy bytes")))
		return
	}
	if result.Error != nil {
		g.copyReply(st, nil, result.Error)
		return
	}
	if !header.List && int64(len(data)) > header.MaxBytes {
		g.copyReply(st, nil, copyError(errors.New("oversized helper copy bytes")))
		return
	}
	g.copyReply(st, data, nil)
}

type copyOutput struct {
	bytes.Buffer
	failed bool
}

func (b *copyOutput) Write(data []byte) (int, error) {
	if len(data) > copyMetadataMax+link.DefaultMaxHeader-b.Len() {
		b.failed = true
		return 0, errors.New("bounded copy helper output exceeded")
	}
	return b.Buffer.Write(data)
}
func (g *Init) copyReply(st *link.Stream, data []byte, err error) {
	result := proto.FileGetResultV1{Version: proto.FileTransferVersionV1, Size: int64(len(data))}
	if err != nil {
		var wire *proto.Error
		if errors.As(err, &wire) {
			result.Error = wire
		} else {
			result.Error = copyError(err)
		}
		result.Size = 0
		data = nil
	}
	_ = st.SetWriteDeadline(time.Now().Add(ctlCloseWait))
	if json.NewEncoder(st).Encode(result) != nil {
		return
	}
	if _, err := st.Write(data); err != nil {
		return
	}
	_ = st.CloseWrite()
}
