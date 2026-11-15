package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const copyListMetadataMax = 1 << 20

func init() { Features = append(Features, proto.FeatureFileTransferV1, "cp") }

type copyFileProgress struct {
	size, offset int64
	done         bool
}
type copyReadPlan struct {
	files map[string]*copyFileProgress
	limit int64
}

func copyRefusal(code, message string) *proto.Error {
	return &proto.Error{Code: code, Message: message, Hint: "Copy ordinary paths from the running sandbox; use pull for work in Git."}
}

func (s *Supervisor) copyRequest(c *client, req proto.Request) bool {
	if req.Op != "cp.list" && req.Op != "cp.read" {
		return false
	}
	c.copyMu.Lock()
	if c.copyBusy {
		c.copyMu.Unlock()
		c.reply(req.ID, nil, copyRefusal("cp_busy", "the previous copy request has not joined"))
		return true
	}
	c.copyBusy = true
	c.copyMu.Unlock()
	root := s.owner()
	ctx, finish, err := root.beginFileTransfer(c)
	if err != nil {
		c.copyMu.Lock()
		c.copyBusy = false
		c.copyMu.Unlock()
		c.reply(req.ID, nil, copyRefusal("sandbox_stopped", "the sandbox is not running"))
		return true
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		defer finish()
		defer cancel()
		var value any
		var err error
		if req.Op == "cp.list" {
			value, err = root.copyList(ctx, c, req.Args, s.id)
		} else {
			value, err = root.copyRead(ctx, c, req.Args, s.id)
		}
		c.copyMu.Lock()
		c.copyBusy = false
		c.copyMu.Unlock()
		if err != nil {
			c.reply(req.ID, nil, toProtoErr(err))
		} else {
			c.reply(req.ID, value, nil)
		}
	}()
	return true
}

func (s *Supervisor) copyPolicyLimit() (int64, error) {
	s.mu.Lock()
	var policy *api.Policy
	if s.m.Policy != nil {
		clone := api.ClonePolicy(*s.m.Policy)
		policy = &clone
	}
	s.mu.Unlock()
	return filecopy.CopyMax(policy)
}

func (s *Supervisor) copyGuest(ctx context.Context, header proto.FileGetHeaderV1) (data []byte, err error) {
	stream, err := s.link.Open(ctx, link.KindGet, header)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeErr := stream.Close()
		if !errors.Is(closeErr, io.EOF) {
			err = errors.Join(err, closeErr)
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = stream.SetDeadline(time.Now()); _ = stream.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := stream.CloseWrite(); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(stream)
	line, err := proto.ReadJSONLine(reader)
	var result proto.FileGetResultV1
	bound := header.MaxBytes
	if header.List {
		bound = copyListMetadataMax
	}
	if err != nil || len(line) > link.DefaultMaxHeader || !api.ExactJSONFields(line, result) || json.Unmarshal(line, &result) != nil || result.Version != proto.FileTransferVersionV1 || result.Size < 0 || result.Size > bound || result.Error != nil && result.Size != 0 {
		return nil, copyRefusal("cp_path", "the guest returned an invalid copy result")
	}
	data, err = io.ReadAll(io.LimitReader(reader, result.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != result.Size {
		return nil, copyRefusal("cp_too_big", "the guest copy differs from its declared byte count")
	}
	if result.Error != nil {
		code := result.Error.Code
		switch code {
		case "cp_path", "cp_git", "cp_exists", "cp_too_big", "cp_changed", "cp_policy":
		default:
			code = "cp_path"
		}
		return nil, copyRefusal(code, "the guest refused the selected ordinary copy path")
	}
	return data, ctx.Err()
}

func (s *Supervisor) copyList(ctx context.Context, c *client, raw json.RawMessage, session string) (proto.CopyListResultV1, error) {
	var args proto.CopyListArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != proto.FileTransferVersionV1 || args.ID != session {
		return proto.CopyListResultV1{}, copyRefusal("cp_path", "invalid copy list request")
	}
	c.copyMu.Lock()
	exists := c.copyPlan != nil
	c.copyMu.Unlock()
	if exists {
		return proto.CopyListResultV1{}, copyRefusal("cp_busy", "this connection already owns its selected copy")
	}
	top, relative, err := filecopy.GuestPath(args.Path)
	if err != nil {
		return proto.CopyListResultV1{}, err
	}
	selected := path.Join(top, relative)
	limit, err := s.copyPolicyLimit()
	if err != nil {
		return proto.CopyListResultV1{}, err
	}
	data, err := s.copyGuest(ctx, proto.FileGetHeaderV1{Version: proto.FileTransferVersionV1, Path: selected, List: true})
	if err != nil {
		return proto.CopyListResultV1{}, err
	}
	var result proto.CopyListResultV1
	if len(data) > copyListMetadataMax || !api.ExactJSONFields(data, result) || json.Unmarshal(data, &result) != nil || result.Version != proto.FileTransferVersionV1 || filecopy.Name(result.Name) != nil || result.Name != path.Base(selected) || len(result.Entries) > 10000 {
		return result, copyRefusal("cp_path", "the guest returned an invalid copy listing")
	}
	plan := &copyReadPlan{files: map[string]*copyFileProgress{}, limit: limit}
	seen := map[string]bool{}
	var total int64
	clean := result.Entries[:0]
	for _, entry := range result.Entries {
		if api.CleanText(entry.Path, len(entry.Path)) != entry.Path {
			continue
		}
		if filecopy.Relative(entry.Path) != nil || seen[strings.ToLower(entry.Path)] || entry.Directory != (entry.Mode == "directory") || entry.Size < 0 {
			return result, copyRefusal("cp_path", "the guest returned ambiguous copy names or types")
		}
		seen[strings.ToLower(entry.Path)] = true
		switch entry.Mode {
		case "directory", "symlink", "special":
			if entry.Size != 0 {
				return result, copyRefusal("cp_path", "a non-file copy entry declares payload bytes")
			}
		case "file":
			if entry.Size > limit-total {
				return result, copyRefusal("cp_too_big", "the selected copy exceeds sandbox.cp_max")
			}
			total += entry.Size
			name := selected
			if result.Directory {
				name = path.Join(selected, entry.Path)
			} else if entry.Path != result.Name {
				return result, copyRefusal("cp_path", "the selected file listing changed its basename")
			}
			plan.files[name] = &copyFileProgress{size: entry.Size}
		default:
			return result, copyRefusal("cp_path", "the guest returned an unknown copy type")
		}
		clean = append(clean, entry)
	}
	result.Entries = clean
	if !result.Directory && len(result.Entries) != 1 {
		return result, copyRefusal("cp_path", "the selected file has an ambiguous listing")
	}
	c.copyMu.Lock()
	c.copyPlan = plan
	c.copyMu.Unlock()
	return result, ctx.Err()
}

func (s *Supervisor) copyRead(ctx context.Context, c *client, raw json.RawMessage, session string) (proto.CopyReadResultV1, error) {
	var args proto.CopyReadArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != proto.FileTransferVersionV1 || args.ID != session || args.Offset < 0 || args.MaxBytes <= 0 || args.MaxBytes > proto.FileTransferChunkMaxV1 {
		return proto.CopyReadResultV1{}, copyRefusal("cp_path", "invalid copy read request")
	}
	top, relative, err := filecopy.GuestPath(args.Path)
	if err != nil {
		return proto.CopyReadResultV1{}, err
	}
	selected := path.Join(top, relative)
	c.copyMu.Lock()
	plan := c.copyPlan
	var file *copyFileProgress
	if plan != nil {
		file = plan.files[selected]
	}
	c.copyMu.Unlock()
	if file == nil || file.done || args.Offset != file.offset || args.Offset > file.size || args.MaxBytes > plan.limit {
		return proto.CopyReadResultV1{}, copyRefusal("cp_path", "the read differs from this connection's original copy listing")
	}
	data, err := s.copyGuest(ctx, proto.FileGetHeaderV1{Version: proto.FileTransferVersionV1, Path: selected, Offset: args.Offset, MaxBytes: args.MaxBytes})
	if err != nil {
		return proto.CopyReadResultV1{}, err
	}
	if int64(len(data)) > file.size-file.offset || len(data) == 0 && file.offset < file.size {
		return proto.CopyReadResultV1{}, copyRefusal("cp_too_big", "the file differs from its original listed size")
	}
	c.copyMu.Lock()
	file.offset += int64(len(data))
	if len(data) == 0 && file.offset == file.size {
		file.done = true
	}
	done := file.done
	c.copyMu.Unlock()
	if done {
		s.audit.write("cp_out", map[string]any{"path": selected, "size": file.size})
	}
	return proto.CopyReadResultV1{Version: proto.FileTransferVersionV1, Data: data}, ctx.Err()
}
