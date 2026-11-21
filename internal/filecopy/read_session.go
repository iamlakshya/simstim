// Package filecopy implements rooted file copies without archive extraction.
package filecopy

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ReadListMetadataMaxV1 keeps copy listings at the existing 1 MiB bound.
const ReadListMetadataMaxV1 = 1 << 20

type readFileProgressV1 struct {
	size, offset int64
	done         bool
}

// ReadSessionV1 validates one connection's original copy selection. It owns
// protocol progress only; the caller retains the actual live or stopped
// native source and its operation until physical cleanup has completed.
type ReadSessionV1 struct {
	mu        sync.Mutex
	session   string
	limit     int64
	get       func(context.Context, proto.FileGetHeaderV1) ([]byte, error)
	files     map[string]*readFileProgressV1
	completed func(string, int64)
}

func NewReadSessionV1(session string, limit int64, get func(context.Context, proto.FileGetHeaderV1) ([]byte, error), completed func(string, int64)) (*ReadSessionV1, error) {
	if !api.ValidSessionID(session) || limit <= 0 || get == nil {
		return nil, readRefusalV1("cp_path", "the copy reader has no exact source or policy bound")
	}
	return &ReadSessionV1{session: session, limit: limit, get: get, completed: completed}, nil
}
func readRefusalV1(code, message string) *proto.Error {
	return &proto.Error{Code: code, Message: message, Hint: "Copy ordinary paths from the selected sandbox; use pull for work in Git."}
}
func (reader *ReadSessionV1) List(ctx context.Context, raw json.RawMessage) (proto.CopyListResultV1, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return proto.CopyListResultV1{}, err
	}
	var args proto.CopyListArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != proto.FileTransferVersionV1 || args.ID != reader.session {
		return proto.CopyListResultV1{}, readRefusalV1("cp_path", "invalid copy list request")
	}
	exists := reader.files != nil
	if exists {
		return proto.CopyListResultV1{}, readRefusalV1("cp_busy", "this connection already owns its selected copy")
	}
	top, relative, err := GuestPath(args.Path)
	if err != nil {
		return proto.CopyListResultV1{}, err
	}
	selected := path.Join(top, relative)
	limit := reader.limit
	data, err := reader.get(ctx, proto.FileGetHeaderV1{Version: proto.FileTransferVersionV1, Path: selected, List: true})
	if err != nil {
		return proto.CopyListResultV1{}, err
	}
	var result proto.CopyListResultV1
	if len(data) > ReadListMetadataMaxV1 || !api.ExactJSONFields(data, result) || json.Unmarshal(data, &result) != nil || result.Version != proto.FileTransferVersionV1 || Name(result.Name) != nil || result.Name != path.Base(selected) || len(result.Entries) > 10000 {
		return result, readRefusalV1("cp_path", "the guest returned an invalid copy listing")
	}
	files := map[string]*readFileProgressV1{}
	seen := map[string]bool{}
	var total int64
	clean := result.Entries[:0]
	for _, entry := range result.Entries {
		if api.CleanText(entry.Path, len(entry.Path)) != entry.Path {
			continue
		}
		if Relative(entry.Path) != nil || seen[strings.ToLower(entry.Path)] || entry.Directory != (entry.Mode == "directory") || entry.Size < 0 {
			return result, readRefusalV1("cp_path", "the guest returned ambiguous copy names or types")
		}
		seen[strings.ToLower(entry.Path)] = true
		switch entry.Mode {
		case "directory", "symlink", "special":
			if entry.Size != 0 {
				return result, readRefusalV1("cp_path", "a non-file copy entry declares payload bytes")
			}
		case "file":
			if entry.Size > limit-total {
				return result, readRefusalV1("cp_too_big", "the selected copy exceeds sandbox.cp_max")
			}
			total += entry.Size
			name := selected
			if result.Directory {
				name = path.Join(selected, entry.Path)
			} else if entry.Path != result.Name {
				return result, readRefusalV1("cp_path", "the selected file listing changed its basename")
			}
			files[name] = &readFileProgressV1{size: entry.Size}
		default:
			return result, readRefusalV1("cp_path", "the guest returned an unknown copy type")
		}
		clean = append(clean, entry)
	}
	result.Entries = clean
	if !result.Directory && len(result.Entries) != 1 {
		return result, readRefusalV1("cp_path", "the selected file has an ambiguous listing")
	}
	reader.files = files
	return result, ctx.Err()
}

func (reader *ReadSessionV1) Read(ctx context.Context, raw json.RawMessage) (proto.CopyReadResultV1, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return proto.CopyReadResultV1{}, err
	}
	var args proto.CopyReadArgsV1
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || args.Version != proto.FileTransferVersionV1 || args.ID != reader.session || args.Offset < 0 || args.MaxBytes <= 0 || args.MaxBytes > proto.FileTransferChunkMaxV1 {
		return proto.CopyReadResultV1{}, readRefusalV1("cp_path", "invalid copy read request")
	}
	top, relative, err := GuestPath(args.Path)
	if err != nil {
		return proto.CopyReadResultV1{}, err
	}
	selected := path.Join(top, relative)
	file := reader.files[selected]
	if file == nil || file.done || args.Offset != file.offset || args.Offset > file.size || args.MaxBytes > reader.limit {
		return proto.CopyReadResultV1{}, readRefusalV1("cp_path", "the read differs from this connection's original copy listing")
	}
	data, err := reader.get(ctx, proto.FileGetHeaderV1{Version: proto.FileTransferVersionV1, Path: selected, Offset: args.Offset, MaxBytes: args.MaxBytes})
	if err != nil {
		return proto.CopyReadResultV1{}, err
	}
	if int64(len(data)) > file.size-file.offset || len(data) == 0 && file.offset < file.size {
		return proto.CopyReadResultV1{}, readRefusalV1("cp_too_big", "the file differs from its original listed size")
	}
	file.offset += int64(len(data))
	if len(data) == 0 && file.offset == file.size {
		file.done = true
		if reader.completed != nil {
			reader.completed(selected, file.size)
		}
	}

	return proto.CopyReadResultV1{Version: proto.FileTransferVersionV1, Data: data}, ctx.Err()
}

// ReadErrorV1 preserves copy codes and hints at both actual RPC boundaries.
func ReadErrorV1(err error) error {
	if err == nil {
		return nil
	}
	var wire *proto.Error
	if errors.As(err, &wire) {
		return err
	}
	var copied *Error
	if errors.As(err, &copied) {
		return &proto.Error{Code: copied.Code, Message: copied.Message, Hint: copied.Hint}
	}
	return readRefusalV1("cp_path", err.Error())
}
