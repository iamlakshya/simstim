package host

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// MaxUpload is the largest file a client may upload.
const MaxUpload = 16 << 20

// uploadIdle is how long an upload waits for its next frame, in
// nanoseconds. An upload that waits longer, as one that its client gave up
// at a cancel, is reclaimed: its file is closed and removed.
var uploadIdle atomic.Int64

// afterUploadUntracked runs in tests right after an upload is removed from
// b.uploads and b.umu is unlocked, before its file is removed or finalised.
var afterUploadUntracked atomic.Pointer[func()]

// beforeUploadReclaimWait runs in tests right before reclaimUploadsAt(true)
// waits on b.uploadsWG.
var beforeUploadReclaimWait atomic.Pointer[func()]

func runAfterUploadUntracked() {
	if f := afterUploadUntracked.Load(); f != nil && *f != nil {
		(*f)()
	}
}

func runBeforeUploadReclaimWait() {
	if f := beforeUploadReclaimWait.Load(); f != nil && *f != nil {
		(*f)()
	}
}

func init() { uploadIdle.Store(int64(2 * time.Minute)) }

// upload is one upload that has begun and not ended: its temporary file in
// the session's paste folder, and the hash of what came so far.
type upload struct {
	session string
	name    string
	size    int64
	f       *os.File
	n       int64
	h       hash.Hash
	// For walled sessions, the upload streams through the supervisor socket
	// instead of writing directly to paths.PasteDir on the host.
	supMu     sync.Mutex
	supConn   net.Conn
	supW      *proto.Writer
	supR      *bufio.Reader
	supFailed atomic.Bool
	// idle reclaims the upload when no frame of it comes for uploadIdle.
	idle *time.Timer
}

// drop closes the upload's file or supervisor stream and removes any temp file.
func (u *upload) drop() {
	u.idle.Stop()
	if u.supConn != nil {
		_ = u.supConn.Close()
		return
	}
	if u.f != nil {
		_ = u.f.Close()
		_ = os.Remove(u.f.Name())
	}
}

// reclaimUploadsAt drops every upload that has begun and not ended. When
// stop is true, the bridge takes no more uploads after it and waits until
// every in-flight removal or finalisation has finished on disk.
func (b *bridge) reclaimUploadsAt(stop bool) {
	b.umu.Lock()
	if stop {
		b.uploadsDone = true
	}
	gone := b.uploads
	b.uploads = map[uint64]*upload{}
	b.umu.Unlock()
	if len(gone) > 0 {
		runAfterUploadUntracked()
	}
	for _, u := range gone {
		u.drop()
		b.uploadsWG.Done()
	}
	if stop {
		runBeforeUploadReclaimWait()
		b.uploadsWG.Wait()
	}
}

func (b *bridge) reclaimUploads() { b.reclaimUploadsAt(false) }

// reclaimBridgesUploads drops the unfinished uploads of every bridge of
// this process, and takes no more, as one that exits without its defers
// does first.
func reclaimBridgesUploads() {
	liveBridges.Range(func(k, _ any) bool {
		k.(*bridge).reclaimUploadsAt(true)
		return true
	})
}

// reclaimUpload drops the upload id, if it is still u.
func (b *bridge) reclaimUpload(id uint64, u *upload) {
	b.umu.Lock()
	if b.uploads[id] != u {
		b.umu.Unlock()
		return
	}
	delete(b.uploads, id)
	b.umu.Unlock()
	runAfterUploadUntracked()
	u.drop()
	b.uploadsWG.Done()
}

func (b *bridge) uploadBegin(p []byte) {
	var u proto.UploadBegin
	if err := json.Unmarshal(p, &u); err != nil {
		return
	}
	fail := func(err error) { b.reply(u.ID, nil, err) }
	if !validID(u.Session) {
		fail(UserError("not_found", "no session "+u.Session, ""))
		return
	}
	if u.Size < 0 || u.Size > MaxUpload {
		fail(UserError("too_big", "the file is larger than 16 MiB", ""))
		return
	}
	// A client sends one upload at a time: one that began before and never
	// ended, its client gave up.
	b.reclaimUploads()
	if m, err := LoadManifest(u.Session); err == nil && isWalled(m) {
		b.uploadBeginWalled(u, p)
		return
	}
	dir := paths.PasteDir(u.Session)
	if err := paths.EnsurePrivateDir(dir); err != nil {
		fail(err)
		return
	}
	cleanOld(dir, 24*time.Hour)
	b.umu.Lock()
	if b.uploadsDone {
		b.umu.Unlock()
		return
	}
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		b.umu.Unlock()
		fail(err)
		return
	}
	up := &upload{session: u.Session, name: safeName(u.Name), size: u.Size, f: f, h: sha256.New()}
	b.uploads[u.ID] = up
	b.uploadsWG.Add(1)
	up.idle = time.AfterFunc(time.Duration(uploadIdle.Load()), func() { b.reclaimUpload(u.ID, up) })
	b.umu.Unlock()
}

func (b *bridge) uploadChunk(p []byte) {
	id, data, err := proto.DecodeChunk(p)
	if err != nil {
		return
	}
	b.umu.Lock()
	u := b.uploads[id]
	if u == nil {
		b.umu.Unlock()
		return
	}
	u.idle.Reset(time.Duration(uploadIdle.Load()))
	u.n += int64(len(data))
	if u.n > MaxUpload {
		b.umu.Unlock()
		return
	}
	if u.supW != nil {
		// Release b.umu before writing to the supervisor socket so reclaimUpload
		// and bridge shutdown can never deadlock behind a stalled guest upload.
		b.umu.Unlock()
		if u.supFailed.Load() {
			return
		}
		u.supMu.Lock()
		_ = u.supConn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if err := u.supW.Write(proto.TUploadChunk, p); err != nil {
			u.supFailed.Store(true)
			_ = u.supConn.Close()
		}
		u.supMu.Unlock()
		return
	}
	_, _ = u.f.Write(data)
	_, _ = u.h.Write(data)
	b.umu.Unlock()
}

func (b *bridge) uploadEnd(p []byte) {
	var e proto.UploadEnd
	if err := json.Unmarshal(p, &e); err != nil {
		return
	}
	b.umu.Lock()
	u := b.uploads[e.ID]
	delete(b.uploads, e.ID)
	b.umu.Unlock()
	if u == nil {
		b.reply(e.ID, nil, UserError("upload", "unknown upload", ""))
		return
	}
	if u.supW != nil {
		b.uploadEndWalled(u, e, p)
		return
	}
	runAfterUploadUntracked()
	u.idle.Stop()
	_ = u.f.Close()
	sum := hex.EncodeToString(u.h.Sum(nil))
	if u.n != u.size || !strings.EqualFold(sum, e.SHA256) {
		_ = os.Remove(u.f.Name())
		b.uploadsWG.Done()
		b.reply(e.ID, nil, UserError("upload", "the upload was damaged in transit", "Try again."))
		return
	}
	final := filepath.Join(filepath.Dir(u.f.Name()), sum[:12]+"-"+u.name)
	if err := os.Rename(u.f.Name(), final); err != nil {
		_ = os.Remove(u.f.Name())
		b.uploadsWG.Done()
		b.reply(e.ID, nil, err)
		return
	}
	_ = os.Chmod(final, 0o600)
	b.uploadsWG.Done()
	b.note("upload", int(u.n))
	b.reply(e.ID, proto.UploadResult{Path: final}, nil)
}

func (b *bridge) uploadBeginWalled(u proto.UploadBegin, raw []byte) {
	fail := func(err error) { b.reply(u.ID, nil, err) }
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(context.Background(), "unix", paths.Socket(u.Session))
	if err != nil {
		fail(UserError("upload", "session "+u.Session+" is not running", "Start or wake the session first."))
		return
	}
	sw := proto.NewWriter(conn)
	sr := bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := sw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		_ = conn.Close()
		fail(err)
		return
	}
	if f, err := proto.Read(sr); err != nil || f.Type != proto.THello {
		_ = conn.Close()
		fail(errors.New("the session's supervisor did not answer"))
		return
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := sw.Write(proto.TUploadBegin, raw); err != nil {
		_ = conn.Close()
		fail(err)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	b.umu.Lock()
	if b.uploadsDone {
		b.umu.Unlock()
		_ = conn.Close()
		return
	}
	up := &upload{session: u.Session, name: safeName(u.Name), size: u.Size, supConn: conn, supW: sw, supR: sr}
	b.uploads[u.ID] = up
	b.uploadsWG.Add(1)
	up.idle = time.AfterFunc(time.Duration(uploadIdle.Load()), func() { b.reclaimUpload(u.ID, up) })
	b.umu.Unlock()
}

func (b *bridge) uploadEndWalled(u *upload, e proto.UploadEnd, raw []byte) {
	runAfterUploadUntracked()
	u.idle.Stop()
	defer func() {
		_ = u.supConn.Close()
		b.uploadsWG.Done()
	}()
	if u.supFailed.Load() {
		b.reply(e.ID, nil, UserError("upload", "the guest upload timed out or closed", "Try again."))
		return
	}
	u.supMu.Lock()
	_ = u.supConn.SetDeadline(time.Now().Add(15 * time.Second))
	writeErr := u.supW.Write(proto.TUploadEnd, raw)
	u.supMu.Unlock()
	if writeErr != nil {
		b.reply(e.ID, nil, writeErr)
		return
	}
	for {
		f, err := proto.Read(u.supR)
		if err != nil {
			b.reply(e.ID, nil, UserError("upload", "the supervisor closed the upload", "Try again."))
			return
		}
		if f.Type != proto.TResponse {
			continue
		}
		var resp proto.Response
		if err := json.Unmarshal(f.Payload, &resp); err != nil {
			b.reply(e.ID, nil, err)
			return
		}
		if !resp.OK {
			if resp.Error != nil {
				b.reply(e.ID, nil, resp.Error)
			} else {
				b.reply(e.ID, nil, UserError("upload", "the upload failed", "Try again."))
			}
			return
		}
		var res proto.UploadResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			b.reply(e.ID, nil, err)
			return
		}
		b.note("upload", int(u.n))
		b.reply(e.ID, res, nil)
		return
	}
}
