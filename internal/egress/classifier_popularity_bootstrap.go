package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// RunPopularityV1 starts only the internal inherited popularity layout.
// Descriptor numbers are restrictions; actual sealed FDs remain mandatory.
func RunPopularityV1(id string, base int) error {
	if !api.ValidSessionID(id) || base < 4 || base > 64 {
		return popularityErrorV1()
	}
	return RunWithOptions(id, Options{Popularity: &PopularitySourceV1{inheritedBase: base, sessionID: id}})
}

// The process has already made itself non-dumpable. This runs before fd3
// connection setup and before any control init or sandbox-time lookup.
func adoptPopularityBootstrapV1(opt *Options) (func(), error) {
	if opt == nil || opt.Popularity == nil || opt.Popularity.inheritedBase == 0 {
		return func() {}, nil
	}
	base, id := opt.Popularity.inheritedBase, opt.Popularity.sessionID
	if base < 4 || base > 64 || !api.ValidSessionID(id) {
		return nil, popularityErrorV1()
	}
	file := os.NewFile(uintptr(base), "classifier-popularity")
	header := os.NewFile(uintptr(base+1), "classifier-popularity-bootstrap")
	if file == nil || header == nil {
		if file != nil {
			_ = file.Close()
		}
		if header != nil {
			_ = header.Close()
		}
		return nil, popularityErrorV1()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = header.Close(); close(closed) })
	reader, err := adoptPopularityFilesV1(ctx, id, file, header)
	_ = header.Close()
	if !stop() {
		<-closed
	}
	if err != nil {
		return nil, err
	}
	opt.Popularity = reader
	return func() { _ = reader.Close() }, nil
}

// The actual file is consumed on both success and failure. The caller owns
// and closes the bounded bootstrap reader; no path is opened here.
func adoptPopularityFilesV1(ctx context.Context, id string, file *os.File, header io.Reader) (*PopularitySourceV1, error) {
	if file == nil {
		return nil, popularityErrorV1()
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || !api.ValidSessionID(id) || header == nil {
		return nil, popularityErrorV1()
	}
	raw, err := io.ReadAll(io.LimitReader(header, maxPopularityHeaderV1+1))
	if err != nil || len(raw) == 0 || len(raw) > maxPopularityHeaderV1 || !api.ExactJSONFields(raw, popularityBootstrapV1{}) {
		return nil, popularityErrorV1()
	}
	fields, fieldErr := api.JSONObjectFields(raw, "v", "session_id", "selected", "original", "snapshot", "digest")
	if fieldErr != nil || len(fields) != 6 {
		return nil, popularityErrorV1()
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return nil, popularityErrorV1()
		}
	}
	for _, name := range []string{"original", "snapshot"} {
		nested, err := api.JSONObjectFields(fields[name], "Device", "Inode", "Size", "Modified", "Changed", "Mode")
		if err != nil || len(nested) != 6 {
			return nil, popularityErrorV1()
		}
		for _, field := range nested {
			if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return nil, popularityErrorV1()
			}
		}
	}
	var meta popularityBootstrapV1
	if json.Unmarshal(raw, &meta) != nil || meta.Version != 1 || meta.SessionID != id || !ValidPopularitySelectionV1(meta.Selected) || meta.Original.Size < 0 || meta.Original.Size > maxPopularityBytesV1 || meta.Original.Size != meta.Snapshot.Size || len(meta.Digest) != 64 {
		return nil, popularityErrorV1()
	}
	version, err := popularitySnapshotVersionV1(file)
	if err != nil || version != meta.Snapshot {
		return nil, popularityErrorV1()
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(hash, &popularityContextReaderV1{ctx: ctx, reader: io.NewSectionReader(file, 0, meta.Snapshot.Size)}, make([]byte, 32<<10))
	if err != nil || count != meta.Snapshot.Size || hex.EncodeToString(hash.Sum(nil)) != meta.Digest || ctx.Err() != nil {
		return nil, popularityErrorV1()
	}
	popularityCloseOnExecV1(file)
	keep = true
	return &PopularitySourceV1{file: file, sessionID: id, selected: meta.Selected, version: version}, nil
}
