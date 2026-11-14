package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
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
	// idle reclaims the upload when no frame of it comes for uploadIdle.
	idle *time.Timer
}

// drop closes the upload's file and removes it.
func (u *upload) drop() {
	u.idle.Stop()
	_ = u.f.Close()
	_ = os.Remove(u.f.Name())
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
	defer b.umu.Unlock()
	u := b.uploads[id]
	if u == nil {
		return
	}
	u.idle.Reset(time.Duration(uploadIdle.Load()))
	u.n += int64(len(data))
	if u.n > MaxUpload {
		return
	}
	_, _ = u.f.Write(data)
	_, _ = u.h.Write(data)
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
