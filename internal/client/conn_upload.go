package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/proto"
)

// uploadTurn is the turn of whole uploads on c: the host takes one upload
// at a time from a connection, and drops an unfinished one when the next
// begins (host's bridge_upload.go).
func (c *Conn) uploadTurn() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.uploading == nil {
		c.uploading = make(chan struct{}, 1)
	}
	return c.uploading
}

// Upload sends a file into a session's paste folder and returns its path
// on the host. As Call, it sends nothing after ctx has ended. A cancel in
// the middle stops before the next frame: the host never gets the end of
// the upload, and reclaims it. Uploads on c take turns, whole: one waits
// for the one before it, until ctx ends.
func (c *Conn) Upload(ctx context.Context, session, name string, data []byte) (string, error) {
	turn := c.uploadTurn()
	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-turn }()
	ctx, release, err := c.beginGrant(ctx, "image paste", false)
	if err != nil {
		return "", err
	}
	defer release()
	tier, creation, err := EffectiveSessionTier(ctx, c.Host, session, c.HostID())
	if err != nil {
		return "", err
	}
	if err := checkPasteCreationPolicy(c, creation); err != nil {
		return "", err
	}
	o := Outbound{Kind: OutboundPaste}
	if err := Allow(c.Host, tier, o, LoadSettings()); err != nil {
		return "", err
	}
	pasteContext, endPasteProof := withLegacyPasteWriteProof(ctx, c, session, tier)
	err = RecordGrantContext(pasteContext, c.Host, c.HostID(), o)
	endPasteProof()
	if err != nil {
		return "", err
	}
	id := c.nextID.Add(1)
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return "", c.err
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return "", err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	failed := func() (string, error) {
		c.drop(id)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrClosed
	}
	requestStage("upload begin")
	if err := c.w.WriteJSONCtx(ctx, proto.TUploadBegin, proto.UploadBegin{ID: id, Session: session, Name: name, Size: int64(len(data))}); err != nil {
		return failed()
	}
	release()
	for off := 0; off < len(data); off += 256 << 10 {
		end := off + 256<<10
		if end > len(data) {
			end = len(data)
		}
		requestStage("upload chunk")
		if err := c.w.WriteCtx(ctx, proto.TUploadChunk, proto.EncodeChunk(id, data[off:end])); err != nil {
			return failed()
		}
	}
	sum := sha256.Sum256(data)
	requestStage("upload end")
	if err := c.w.WriteJSONCtx(ctx, proto.TUploadEnd, proto.UploadEnd{ID: id, SHA256: hex.EncodeToString(sum[:])}); err != nil {
		return failed()
	}
	requestStage("upload sent")
	select {
	case <-ctx.Done():
		c.drop(id) // its answer, if one comes, goes nowhere
		return "", ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return "", ErrClosed
		}
		if !resp.OK {
			if resp.Error != nil {
				return "", hostErrorOn(c.Host.Name, resp.Error)
			}
			return "", errors.New("upload failed")
		}
		var r proto.UploadResult
		if err := json.Unmarshal(resp.Result, &r); err != nil {
			return "", fmt.Errorf("read the answer to an upload: %w", err)
		}
		return r.Path, nil
	}
}
