package filecopy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func ReadGuestV1(ctx context.Context, open func(context.Context, link.Kind, any) (*link.Stream, error), header proto.FileGetHeaderV1) (data []byte, err error) {
	stream, err := open(ctx, link.KindGet, header)
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
		bound = ReadListMetadataMaxV1
	}
	if err != nil || len(line) > link.DefaultMaxHeader || !api.ExactJSONFields(line, result) || json.Unmarshal(line, &result) != nil || result.Version != proto.FileTransferVersionV1 || result.Size < 0 || result.Size > bound || result.Error != nil && result.Size != 0 {
		return nil, readRefusalV1("cp_path", "the guest returned an invalid copy result")
	}
	data, err = io.ReadAll(io.LimitReader(reader, result.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != result.Size {
		return nil, readRefusalV1("cp_too_big", "the guest copy differs from its declared byte count")
	}
	if result.Error != nil {
		code := result.Error.Code
		switch code {
		case "cp_path", "cp_git", "cp_exists", "cp_too_big", "cp_changed", "cp_policy":
		default:
			code = "cp_path"
		}
		return nil, readRefusalV1(code, "the guest refused the selected ordinary copy path")
	}
	return data, ctx.Err()
}
