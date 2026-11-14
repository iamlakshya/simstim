package cachefs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type domainBinding struct {
	Version int    `json:"version"`
	Domain  Domain `json:"domain"`
}

// ReadDomainBinding reads an existing anchored regular single-link 0600
// descriptor. Found is false only for a physically empty file. It creates,
// truncates and repairs nothing. The caller retains its exact native lease.
func ReadDomainBinding(ctx context.Context, root *os.Root, leaf string, file *os.File) (Domain, bool, error) {
	var domain Domain
	if err := ctx.Err(); err != nil {
		return domain, false, err
	}
	info, err := ValidateFile(root, leaf, file, 0o600)
	if err != nil {
		return domain, false, err
	}
	if info.Size() == 0 {
		return domain, false, nil
	}
	if info.Size() > proto.MaxFrame {
		return domain, false, domainError("the existing host-domain binding exceeds the protocol metadata bound")
	}
	reader := domainReader{ctx: ctx, reader: io.NewSectionReader(file, 0, int64(proto.MaxFrame)+1)}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return domain, false, err
	}
	if len(raw) > proto.MaxFrame {
		return domain, false, domainError("the host-domain binding grew beyond its metadata bound")
	}
	fields, err := api.ProjectedJSONObjectFields(ctx, raw, "version", "domain")
	var version int
	if err != nil || len(fields) != 2 || json.Unmarshal(fields["version"], &version) != nil || version != 1 {
		return domain, false, domainContextError(ctx, "the existing host-domain binding is malformed or has an unknown version")
	}
	domain, err = decodeDomainContext(ctx, fields["domain"])
	if err != nil {
		return domain, false, err
	}
	current, err := ValidateFile(root, leaf, file, 0o600)
	if err != nil || !os.SameFile(info, current) || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
		return domain, false, domainContextError(ctx, "the host-domain descriptor changed during validation")
	}
	return domain, true, ctx.Err()
}

// InitialiseDomainBinding writes only a physically empty existing sentinel.
// The caller must hold its exact native lock and prove a new owned empty
// domain or precise canonical legacy State owner. It never adopts other files.
// Existing nonempty, partial or unknown contents are never truncated or reset.
func InitialiseDomainBinding(ctx context.Context, root *os.Root, leaf string, file *os.File, domain Domain) (Reference, error) {
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}
	if err := domain.Validate(); err != nil {
		return Reference{}, err
	}
	if err := ValidateRoot(root); err != nil {
		return Reference{}, err
	}
	before, err := CaptureFile(root, leaf, file, 0o600)
	if err != nil {
		return Reference{}, err
	}
	if before.Size != 0 {
		return Reference{}, domainError("a nonempty sentinel cannot acquire or replace host-domain authority")
	}
	data, err := json.Marshal(domainBinding{Version: 1, Domain: domain})
	if err != nil {
		return Reference{}, domainError("cannot encode the canonical host-domain binding")
	}
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}
	if err := before.CheckFile(root, leaf, file, 0o600); err != nil {
		return Reference{}, err
	}
	data = append(data, '\n')
	n, err := file.WriteAt(data, 0)
	if err != nil {
		return Reference{}, err
	}
	if n != len(data) {
		return Reference{}, io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return Reference{}, err
	}
	dir, err := root.Open(".")
	if err != nil {
		return Reference{}, err
	}
	err = SyncDirectory(dir)
	closeErr := dir.Close()
	if err != nil {
		return Reference{}, err
	}
	if closeErr != nil {
		return Reference{}, closeErr
	}
	actual, found, err := ReadDomainBinding(ctx, root, leaf, file)
	if err != nil || !found || actual != domain {
		return Reference{}, errors.Join(err, domainContextError(ctx, "the new host-domain binding did not survive its synced reread"))
	}
	if err := ValidateRoot(root); err != nil {
		return Reference{}, err
	}
	return CaptureFile(root, leaf, file, 0o600)
}

type domainReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader domainReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := reader.reader.Read(data)
	if stopped := reader.ctx.Err(); stopped != nil {
		return n, stopped
	}
	return n, err
}
