package supervisor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	// uploadClientReadTimeout bounds waiting for the next upload frame from the bridge.
	uploadClientReadTimeout = 30 * time.Second
	// defaultUploadStreamTimeout bounds writes to and confirmation reads from the
	// guest KindPut stream so a stalled guest cannot leak a stream slot forever.
	defaultUploadStreamTimeout = 15 * time.Second
)

func (s *Supervisor) uploadStreamTimeout() time.Duration {
	if s.opt.UploadTimeout > 0 {
		return s.opt.UploadTimeout
	}
	return defaultUploadStreamTimeout
}

// handleUpload receives TUploadBegin, TUploadChunk and TUploadEnd frames on the
// session socket and streams the bytes to guest init over a link.KindPut stream.
func (s *Supervisor) handleUpload(conn net.Conn, r *bufio.Reader, w *proto.Writer, beginPayload []byte) {
	var u proto.UploadBegin
	if json.Unmarshal(beginPayload, &u) != nil {
		return
	}
	if u.Size < 0 || u.Size > host.MaxUpload {
		writeUploadError(w, u.ID, "too_big", "the file is larger than 16 MiB", "Upload a smaller file.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := s.link.Open(ctx, link.KindPut, guest.PutHeader{Name: host.SafeName(u.Name), Size: u.Size})
	cancel()
	if err != nil {
		writeUploadError(w, u.ID, "upload", "could not open guest upload stream", "Check that the session is running.")
		return
	}
	defer st.Close()
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	s.streamUpload(conn, r, w, st, u)
}

func (s *Supervisor) streamUpload(conn net.Conn, r *bufio.Reader, w *proto.Writer, st *link.Stream, u proto.UploadBegin) {
	h := sha256.New()
	var n int64
	streamTimeout := s.uploadStreamTimeout()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(uploadClientReadTimeout))
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TUploadChunk:
			nextN, ok := s.writeUploadChunk(w, st, u.ID, f.Payload, n, streamTimeout, h)
			if !ok {
				return
			}
			n = nextN
		case proto.TUploadEnd:
			s.finishUpload(w, st, u, f.Payload, n, hex.EncodeToString(h.Sum(nil)), streamTimeout)
			return
		default:
			s.refuseFrame("client", f.Type)
			return
		}
	}
}

func (s *Supervisor) writeUploadChunk(w *proto.Writer, st *link.Stream, wantID uint64, payload []byte, n int64, timeout time.Duration, h io.Writer) (int64, bool) {
	id, data, err := proto.DecodeChunk(payload)
	if err != nil || id != wantID {
		return n, false
	}
	n += int64(len(data))
	if n > host.MaxUpload {
		writeUploadError(w, wantID, "too_big", "the file is larger than 16 MiB", "Upload a smaller file.")
		return n, false
	}
	_ = st.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := st.Write(data); err != nil {
		writeUploadError(w, wantID, "upload", "failed to send upload to the guest", "Try again.")
		return n, false
	}
	_, _ = h.Write(data)
	return n, true
}

func (s *Supervisor) finishUpload(w *proto.Writer, st *link.Stream, u proto.UploadBegin, endPayload []byte, n int64, sum string, timeout time.Duration) {
	var e proto.UploadEnd
	if json.Unmarshal(endPayload, &e) != nil || e.ID != u.ID {
		writeUploadError(w, u.ID, "upload", "unknown upload", "Try again.")
		return
	}
	if n != u.Size || !strings.EqualFold(sum, e.SHA256) {
		writeUploadError(w, u.ID, "upload", "the upload was damaged in transit", "Try again.")
		return
	}
	_ = st.SetDeadline(time.Now().Add(timeout))
	if err := st.CloseWrite(); err != nil {
		writeUploadError(w, u.ID, "upload", "could not finish guest upload", "Try again.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(st, 16<<10))
	if err != nil {
		writeUploadError(w, u.ID, "upload", "the guest did not confirm the upload", "Try again.")
		return
	}
	var resp guest.PutResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		writeUploadError(w, u.ID, "upload", "invalid upload reply from guest", "Try again.")
		return
	}
	if resp.Error != nil {
		_ = w.WriteJSON(proto.TResponse, proto.Response{
			ID:    u.ID,
			Error: cleanGuestError(resp.Error, "upload", "the upload failed", "Try again."),
		})
		return
	}
	if !strings.EqualFold(resp.SHA256, sum) {
		writeUploadError(w, u.ID, "upload", "the guest checksum did not match", "Try again.")
		return
	}
	cleanPath := api.CleanText(resp.Path, 512)
	s.audit.Record(AuditEntry{Event: "upload", Session: s.id, Detail: host.SafeName(u.Name)})
	resBytes, _ := json.Marshal(proto.UploadResult{Path: cleanPath})
	_ = w.WriteJSON(proto.TResponse, proto.Response{ID: u.ID, OK: true, Result: resBytes})
}

func writeUploadError(w *proto.Writer, id uint64, code, msg, hint string) {
	_ = w.WriteJSON(proto.TResponse, proto.Response{
		ID:    id,
		Error: &proto.Error{Code: code, Message: msg, Hint: hint},
	})
}
