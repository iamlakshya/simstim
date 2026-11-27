package client

import (
	"context"
	"encoding/json"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/review"
	"github.com/iamlakshya/simstim/internal/proto"
)

type copyChunkReader struct {
	ctx          context.Context
	connection   *Conn
	id, path     string
	size, offset int64
	buffer       []byte
	ended        bool
}

func (r *copyChunkReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if len(r.buffer) > 0 {
		n := copy(data, r.buffer)
		r.buffer = r.buffer[n:]
		return n, nil
	}
	if r.ended {
		return 0, io.EOF
	}
	maxBytes := min(int64(proto.FileTransferChunkMaxV1), r.size-r.offset)
	if maxBytes == 0 {
		maxBytes = 1
	}
	var raw json.RawMessage
	if err := r.connection.Call(r.ctx, "cp.read", proto.CopyReadArgsV1{Version: proto.FileTransferVersionV1, ID: r.id, Path: r.path, Offset: r.offset, MaxBytes: maxBytes}, &raw); err != nil {
		return 0, err
	}
	var result proto.CopyReadResultV1
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != proto.FileTransferVersionV1 || int64(len(result.Data)) > maxBytes || int64(len(result.Data)) > r.size-r.offset {
		return 0, copyClientError("cp_too_big", "the copied file sends more bytes than listed", "Repeat the copy after the sandbox file stops changing.")
	}
	if len(result.Data) == 0 {
		if r.offset != r.size {
			return 0, copyClientError("cp_too_big", "the copied file ended before its listed size", "Repeat the copy after the sandbox file stops changing.")
		}
		r.ended = true
		return 0, io.EOF
	}
	r.offset += int64(len(result.Data))
	r.buffer = result.Data
	return r.Read(data)
}

func copyScanner(values [][]byte) (*review.Scanner, error) {
	rules, err := review.DefaultRules()
	if err != nil {
		return nil, err
	}
	var projects []string
	for _, agent := range agents.Table {
		projects = append(projects, agent.ProjectFiles...)
	}
	return review.New(rules, review.Options{ProjectFiles: projects, Secrets: values, Brand: brand.Name})
}
func copyScan(scanner *review.Scanner, name string, size int64, data []byte, modules [][]byte) ([]review.Finding, error) {
	return scanner.ScanCopy(review.Change{Path: filepath.ToSlash(name), Size: size, Mode: "100644", After: data, Added: data, ModuleValues: modules, ModulesParsed: strings.EqualFold(path.Base(name), ".gitmodules") && len(data) > 0})
}
func uniqueCopyFindings(findings []review.Finding) []review.Finding {
	slices.SortFunc(findings, func(a, b review.Finding) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		if a.Category != b.Category {
			return strings.Compare(a.Category, b.Category)
		}
		return strings.Compare(a.Reason, b.Reason)
	})
	return slices.Compact(findings)
}
func copyConsent(result CopyResult, allow bool) error {
	data, _ := json.Marshal(result)
	for _, finding := range result.Findings {
		if finding.Refused {
			return &CopyReviewError{Result: result, Failure: &UserError{Code: "cp_flagged", Msg: "the copy contains a path or content which cannot be safely published", Hint: "Inspect it in the sandbox and copy ordinary files instead.", Data: data, Kind: KindRefused}}
		}
	}
	if len(result.Findings) > 0 && !allow {
		return &CopyReviewError{Result: result, Failure: &UserError{Code: "cp_flagged", Msg: "the copy contains files that can run code or expose session data", Hint: "Inspect the listed files in the sandbox, then use --allow-flagged to copy them.", Data: data, Kind: KindRefused}}
	}
	return nil
}
