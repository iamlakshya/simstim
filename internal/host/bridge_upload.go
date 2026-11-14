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

	"github.com/iamlakshya/simstim/internal/api"
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
	remote     *holderLine
	stopRemote func() bool
	failed     error
	limit      int64
	session    string
	name       string
	size       int64
	f          *os.File
	n          int64
	h          hash.Hash
	// idle reclaims the upload when no frame of it comes for uploadIdle.
	idle *time.Timer
}

// drop closes the upload's file and removes it.
func (u *upload) drop() {
	if u.idle != nil {
		u.idle.Stop()
	}
	if u.stopRemote != nil {
		u.stopRemote()
	}
	if u.remote != nil {
		_ = u.remote.c.Close()
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
	var finishing []*upload
	if stop {
		for up := range b.finishingUploads {
			finishing = append(finishing, up)
		}
	}
	b.umu.Unlock()
	for _, up := range finishing {
		_ = up.remote.c.Close()
	}
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
	var begin proto.UploadBegin
	if err := json.Unmarshal(p, &begin); err != nil || !api.ExactJSONFields(p, begin) {
		return
	}
	fail := func(err error) { b.reply(begin.ID, nil, err) }
	if !validID(begin.Session) {
		fail(UserError("not_found", "no session "+begin.Session, "List sessions to choose an existing session."))
		return
	}
	// The same lock linearises supervisor admission with EndBridge.
	b.reclaimUploads()
	b.umu.Lock()
	if b.uploadsDone {
		b.umu.Unlock()
		return
	}
	up, limit, err := b.prepareWalledUpload(begin)
	if err != nil {
		b.umu.Unlock()
		fail(err)
		return
	}
	if begin.Size < 0 || begin.Size > limit {
		b.umu.Unlock()
		fail(UserError("too_big", "the file is larger than its upload limit", "Choose a smaller file."))
		return
	}
	if up == nil {
		dir := paths.PasteDir(begin.Session)
		if err := paths.EnsurePrivateDir(dir); err != nil {
			b.umu.Unlock()
			fail(err)
			return
		}
		cleanOld(dir, 24*time.Hour)
		file, err := os.CreateTemp(dir, ".upload-*")
		if err != nil {
			b.umu.Unlock()
			fail(err)
			return
		}
		up = &upload{session: begin.Session, name: safeName(begin.Name), size: begin.Size, f: file}
	}
	up.limit, up.h = limit, sha256.New()
	b.uploads[begin.ID] = up
	b.uploadsWG.Add(1)
	up.idle = time.AfterFunc(time.Duration(uploadIdle.Load()), func() { b.reclaimUpload(begin.ID, up) })
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
	if u.n > u.limit || u.n > u.size {
		u.failed = UserError("upload", "the upload exceeds its declared size", "Upload the file again with its exact size.")
		return
	}
	if u.remote != nil {
		if u.failed == nil {
			_ = u.remote.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			u.failed = u.remote.w.Write(proto.TUploadChunk, p)
		}
	} else if u.failed == nil {
		_, u.failed = u.f.Write(data)
	}
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
	if u != nil && u.remote != nil {
		if b.finishingUploads == nil {
			b.finishingUploads = make(map[*upload]struct{})
		}
		b.finishingUploads[u] = struct{}{}
	}
	b.umu.Unlock()
	if u == nil {
		b.reply(e.ID, nil, UserError("upload", "unknown upload", ""))
		return
	}
	runAfterUploadUntracked()
	u.idle.Stop()
	if u.remote != nil {
		b.finishWalledUpload(e.ID, u, p)
		return
	}
	_ = u.f.Close()
	sum := hex.EncodeToString(u.h.Sum(nil))
	if u.failed != nil || u.n != u.size || !strings.EqualFold(sum, e.SHA256) {
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
