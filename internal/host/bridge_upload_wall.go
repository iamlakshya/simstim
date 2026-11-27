package host

import (
	"context"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, "put") }

// prepareWalledUpload retains one actual supervisor connection until the last
// upload frame and its checked reply. The attach connection stays untouched.
func (b *bridge) prepareWalledUpload(begin proto.UploadBegin) (*upload, int64, error) {
	if err := CheckCaller(); err != nil {
		return nil, 0, err
	}
	m, err := LoadManifest(begin.Session)
	if err != nil || m.ID != begin.Session {
		return nil, 0, UserError("not_found", "the upload's session record is unavailable", "List the sessions and choose an existing session.")
	}
	if begin.Directory && (begin.Path == "" || begin.Size != 0 || begin.Replace) {
		return nil, 0, UserError("cp_path", "invalid empty-directory upload declaration", "Name one explicit guest directory without replacement or file bytes.")
	}
	if m.Wall == nil {
		if begin.Path != "" || begin.Replace || begin.Directory {
			return nil, 0, UserError("not_walled", "explicit file destinations need a sandbox", "Start a walled session before copying files.")
		}
		return nil, MaxUpload, nil
	}
	limit := int64(MaxUpload)
	if begin.Path != "" {
		limit, err = filecopy.CopyMax(m.Policy)
		if err != nil {
			return nil, 0, UserError("cp_max", "the sandbox copy limit is invalid", "Correct sandbox.cp_max in a trusted policy layer before starting the sandbox.")
		}
	}
	if begin.Size < 0 || begin.Size > limit {
		return nil, 0, UserError("too_big", "the file exceeds the session's upload limit", "Choose a smaller file or increase its trusted copy limit before starting the sandbox.")
	}
	line, hello, err := dialSessionHolder(begin.Session, 10*time.Second)
	if err != nil {
		return nil, 0, UserError("sandbox_stopped", "the sandbox is unavailable for an upload", "Resume the session, then upload the file again.")
	}
	required := []string{"put"}
	if begin.Path != "" || begin.Replace || begin.Directory {
		required = append(required, proto.FeatureFileTransferV1)
	}
	if err := requireSupervisorFeatures(hello, required...); err != nil {
		_ = line.c.Close()
		return nil, 0, err
	}
	if begin.Path == "" {
		begin.Name = safeName(begin.Name)
	}
	if err := line.w.WriteJSON(proto.TUploadBegin, begin); err != nil {
		_ = line.c.Close()
		return nil, 0, UserError("upload", "the supervisor did not accept the upload declaration", "Check that the sandbox is running, then upload the file again.")
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	stop := context.AfterFunc(ctx, func() { _ = line.c.Close() })
	return &upload{session: begin.Session, name: begin.Name, size: begin.Size, remote: line, stopRemote: stop}, limit, nil
}

func (b *bridge) finishWalledUpload(id uint64, up *upload, end []byte) {
	defer func() {
		b.umu.Lock()
		delete(b.finishingUploads, up)
		b.umu.Unlock()
	}()
	defer b.uploadsWG.Done()
	defer up.drop()
	if up.failed != nil {
		b.reply(id, nil, UserError("upload", "the sandbox upload connection failed", "Upload the file again after checking that the sandbox is running."))
		return
	}
	_ = up.remote.c.SetDeadline(time.Now().Add(time.Minute))
	if err := up.remote.w.Write(proto.TUploadEnd, end); err != nil {
		b.reply(id, nil, UserError("upload", "the upload could not finish its file stream", "Check that the sandbox is running, then upload the file again."))
		return
	}
	frame, err := proto.Read(up.remote.r)
	var response proto.Response
	if err != nil || frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id {
		b.reply(id, nil, UserError("upload", "the supervisor did not confirm the upload", "Upload the file again after checking the session log."))
		return
	}
	if !response.OK {
		if response.Error == nil {
			response.Error = UserError("upload", "the supervisor refused the upload", "Check the session log, then upload the file again.")
		}
		b.reply(id, nil, response.Error)
		return
	}
	var result proto.UploadResult
	if err := json.Unmarshal(response.Result, &result); err != nil || result.Path == "" {
		b.reply(id, nil, UserError("upload", "the supervisor returned no file path", "Update the helper and resume the session."))
		return
	}
	b.note("upload", int(up.n))
	b.reply(id, result, nil)
}
