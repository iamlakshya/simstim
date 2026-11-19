package supervisor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

type fileTransfer struct {
	cancel context.CancelFunc
	client *client
}

func init() { Features = append(Features, "put") }

func uploadRefusal(message string) *proto.Error {
	return &proto.Error{Code: "upload", Message: message, Hint: "Check the file and the running sandbox, then upload it again."}
}

// beginFileTransfer makes admission and shutdown share the same state lock.
func (s *Supervisor) beginFileTransfer(c *client) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fileTransfersClosed || s.rec.phase != host.LifeRunning {
		return nil, nil, uploadRefusal("the sandbox is not running")
	}
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), 2*time.Minute)
	op := &fileTransfer{cancel: cancel, client: c}
	if s.fileTransfers == nil {
		s.fileTransfers = make(map[*fileTransfer]struct{})
	}
	s.fileTransfers[op] = struct{}{}
	s.fileWG.Add(1)
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.fileTransfers, op)
		s.mu.Unlock()
		s.fileWG.Done()
	}, nil
}

func (s *Supervisor) closeFileTransfers() {
	s.mu.Lock()
	s.fileTransfersClosed = true
	ops := make([]*fileTransfer, 0, len(s.fileTransfers))
	for op := range s.fileTransfers {
		ops = append(ops, op)
	}
	s.mu.Unlock()
	for _, op := range ops {
		op.cancel()
		op.client.close()
	}
	s.fileWG.Wait()
}

// uploadFrame takes one dedicated full-peer connection. It never shares its
// reader with the holder or interprets an attached terminal's bytes.
func (s *Supervisor) uploadFrame(c *client, reader *bufio.Reader, frame proto.Frame, first bool) bool {
	if frame.Type != proto.TUploadBegin {
		return false
	}
	var begin proto.UploadBegin
	if json.Unmarshal(frame.Payload, &begin) != nil {
		return true
	}
	if !first || !api.ExactJSONFields(frame.Payload, begin) || begin.Session != s.id || begin.Size < 0 || filecopy.Name(begin.Name) != nil {
		c.reply(begin.ID, nil, uploadRefusal("invalid dedicated upload declaration"))
		return true
	}
	if begin.Directory && (begin.Path == "" || begin.Size != 0 || begin.Replace) {
		c.reply(begin.ID, nil, uploadRefusal("invalid empty-directory upload"))
		return true
	}
	ctx, finish, err := s.owner().beginFileTransfer(c)
	if err != nil {
		c.reply(begin.ID, nil, toProtoErr(err))
		return true
	}
	defer finish()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, c.close)
	defer stop()
	s.mu.Lock()
	var policy *api.Policy
	if s.m.Policy != nil {
		copy := api.ClonePolicy(*s.m.Policy)
		policy = &copy
	}
	s.mu.Unlock()
	if policy == nil {
		c.reply(begin.ID, nil, uploadRefusal("the running sandbox has no known upload policy"))
		return true
	}
	limit := int64(host.MaxUpload)
	if begin.Path != "" {
		limit, err = filecopy.CopyMax(policy)
		if err == nil {
			_, _, err = filecopy.GuestPath(begin.Path)
		}
	} else if begin.Replace {
		err = uploadRefusal("paste cannot replace a caller-selected path")
	}
	if err != nil || begin.Size > limit {
		c.reply(begin.ID, nil, uploadRefusal("the file exceeds the running sandbox's upload policy"))
		return true
	}
	spool, err := os.CreateTemp(paths.SessionDir(s.id), ".paste-")
	if err != nil {
		c.reply(begin.ID, nil, uploadRefusal("the upload cannot create a private spool"))
		return true
	}
	defer func() { _ = spool.Close(); _ = os.Remove(spool.Name()) }()
	end, err := receiveUpload(ctx, c, reader, begin, spool)
	if err != nil {
		c.reply(begin.ID, nil, toProtoErr(err))
		return true
	}
	if begin.Path == "" && !pasteAny(policy) {
		if err := checkPasteMedia(spool, begin.Size); err != nil {
			c.reply(begin.ID, nil, toProtoErr(err))
			return true
		}
	}
	// After End, any more input, including EOF, ends this dedicated transfer.
	// The same buffered reader is retained, so trailing bytes cannot hide.
	watch := make(chan struct{})
	go func() { defer close(watch); _, _ = reader.ReadByte(); cancel() }()
	defer func() { _ = c.conn.Close(); <-watch }()
	header := proto.FilePutHeaderV1{Version: proto.FileTransferVersionV1, Name: host.NewID() + "-" + begin.Name, Directory: begin.Directory, Size: begin.Size, SHA256: end.SHA256, Path: begin.Path, Replace: begin.Replace}
	target, err := s.putFile(ctx, spool, header)
	if err == nil {
		event := "paste"
		if begin.Path != "" {
			event = "copy_in"
		}
		s.audit.write(event, map[string]any{"name": begin.Name, "bytes": begin.Size, "path": target})
	}
	c.reply(begin.ID, proto.UploadResult{Path: target}, toProtoErrOrNil(err))
	return true
}

func receiveUpload(ctx context.Context, c *client, reader *bufio.Reader, begin proto.UploadBegin, file *os.File) (proto.UploadEnd, error) {
	hash := sha256.New()
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return proto.UploadEnd{}, err
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		frame, err := proto.Read(reader)
		if err != nil {
			return proto.UploadEnd{}, uploadRefusal("the upload ended before its complete file")
		}
		switch frame.Type {
		case proto.TUploadChunk:
			id, data, err := proto.DecodeChunk(frame.Payload)
			if err != nil || id != begin.ID || int64(len(data)) > begin.Size-count {
				return proto.UploadEnd{}, uploadRefusal("invalid upload chunk")
			}
			if _, err := io.MultiWriter(file, hash).Write(data); err != nil {
				return proto.UploadEnd{}, uploadRefusal("the private upload spool could not be written")
			}
			count += int64(len(data))
		case proto.TUploadEnd:
			var end proto.UploadEnd
			if !api.ExactJSONFields(frame.Payload, end) || json.Unmarshal(frame.Payload, &end) != nil || end.ID != begin.ID || count != begin.Size || end.SHA256 != hex.EncodeToString(hash.Sum(nil)) {
				return end, uploadRefusal("the upload length or digest does not match")
			}
			_ = c.conn.SetReadDeadline(time.Time{})
			return end, nil
		default:
			return proto.UploadEnd{}, uploadRefusal("an upload connection carried another operation")
		}
	}
}

func pasteAny(policy *api.Policy) bool {
	return policy != nil && policy.Sandbox.Paste != nil && *policy.Sandbox.Paste == "any"
}

func checkPasteMedia(file *os.File, size int64) error {
	var prefix [512]byte
	n, err := file.ReadAt(prefix[:min(int64(len(prefix)), size)], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return uploadRefusal("the upload could not be checked")
	}
	switch strings.TrimSpace(strings.SplitN(http.DetectContentType(prefix[:n]), ";", 2)[0]) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf":
		return nil
	default:
		return &proto.Error{Code: "paste_type", Message: "the sandbox accepts only images and PDF files", Hint: "Set sandbox.paste to any in a trusted policy layer before starting the sandbox to upload other files."}
	}
}

func (s *Supervisor) putFile(ctx context.Context, file *os.File, header proto.FilePutHeaderV1) (string, error) {
	expected := header.Path
	if expected == "" {
		expected = path.Join(host.GuestRunDir(), "paste", header.SHA256[:12]+"-"+header.Name)
	}
	if header.Path != "" && !header.Directory && s.owner().processCopy != nil {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		return s.owner().processCopy.Receive(ctx, header, file)
	}
	stream, err := s.link.Open(ctx, link.KindPut, header)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.SetDeadline(time.Now()); _ = stream.Close() })
	defer stop()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if _, err := io.CopyN(stream, file, header.Size); err != nil {
		return "", err
	}
	if err := stream.CloseWrite(); err != nil {
		return "", err
	}
	reader := bufio.NewReader(stream)
	line, err := proto.ReadJSONLine(reader)
	var result proto.FilePutResultV1
	if err != nil || len(line) > link.DefaultMaxHeader || !api.ExactJSONFields(line, result) || json.Unmarshal(line, &result) != nil || result.Version != proto.FileTransferVersionV1 || result.Error != nil || result.Path != expected {
		if result.Version == proto.FileTransferVersionV1 && result.Path == "" && result.Error != nil && result.Error.Code == "cp_exists" {
			return "", &proto.Error{Code: "cp_exists", Message: "the copy destination already exists", Hint: "Choose another path, or explicitly use --force to replace the file."}
		}
		return "", uploadRefusal("the guest did not confirm the complete file at its declared path")
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		return "", uploadRefusal("the guest sent trailing file receipt data")
	}
	return expected, ctx.Err()
}

func toProtoErrOrNil(err error) *proto.Error {
	if err == nil {
		return nil
	}
	return toProtoErr(err)
}
