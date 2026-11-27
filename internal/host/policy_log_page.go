package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const auditPageReadBudget = 3 * time.Second

// ReadPolicyLog selects the legacy snapshot or the explicit bounded-page
// response on the same policy.log op. It adds no watcher or persistence.
func ReadPolicyLog(path string, a api.PolicyLogArgs) (any, error) {
	if !a.Page {
		if a.Cursor != "" || a.Limit != 0 {
			return nil, badAuditPageArgs()
		}
		return ReadAuditNetEntries(path, a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), auditPageReadBudget)
	defer cancel()
	page, err := ReadAuditNetPage(ctx, path, a)
	switch {
	case errors.Is(err, context.Canceled):
		return nil, UserError("cancelled", "the policy log read was cancelled", "Start the log read again when ready.")
	case errors.Is(err, context.DeadlineExceeded):
		return nil, UserError("timeout", "the policy log page did not finish in time", "Check the host's storage, then start the log read again.")
	case err != nil:
		return nil, err
	default:
		return page, nil
	}
}

// ReadAuditNetPage returns bounded new records from the trusted append-only
// audit log. It detects observable identity changes and shrinking, not
// arbitrary in-place rewrites by the host or another process of its user.
func ReadAuditNetPage(ctx context.Context, path string, a api.PolicyLogArgs) (api.PolicyLogPage, error) {
	query, cursor, err := prepareAuditPage(a, time.Now().UTC())
	if err != nil {
		return api.PolicyLogPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return api.PolicyLogPage{}, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if cancelled := ctx.Err(); cancelled != nil {
			return api.PolicyLogPage{}, cancelled
		}
		if errors.Is(err, os.ErrNotExist) {
			if cursor.File != "" {
				return api.PolicyLogPage{}, changedAuditPage()
			}
			return finishAuditPage(api.PolicyLogPage{Entries: []api.NetAuditEntry{}}, cursor)
		}
		if cursor.File != "" {
			if info, statErr := os.Lstat(path); statErr == nil {
				identity, identityErr := auditPageFileID(info)
				if identityErr != nil || identity != cursor.File {
					return api.PolicyLogPage{}, changedAuditPage()
				}
			}
		}
		return api.PolicyLogPage{}, auditReadError()
	}
	defer f.Close()
	stop := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stop()
	before, err := f.Stat()
	if err != nil {
		return api.PolicyLogPage{}, auditPageReadError(ctx)
	}
	identity, err := auditPageFileID(before)
	if err != nil {
		if cursor.File != "" {
			return api.PolicyLogPage{}, changedAuditPage()
		}
		return api.PolicyLogPage{}, err
	}
	if cursor.File != "" && (identity != cursor.File || before.Size() < cursor.Size || before.Size() < cursor.Offset) {
		return api.PolicyLogPage{}, changedAuditPage()
	}
	cursor.File = identity
	readBytes := min(before.Size()-cursor.Offset, int64(2*api.PolicyLogMaxDataBytes))
	reader := bufio.NewReaderSize(io.NewSectionReader(f, cursor.Offset, readBytes), 8<<10)
	page, next, err := scanAuditPage(ctx, reader, cursor, query, before.Size())
	if err != nil {
		return api.PolicyLogPage{}, err
	}
	after, err := f.Stat()
	if err != nil {
		return api.PolicyLogPage{}, auditPageReadError(ctx)
	}
	current, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return api.PolicyLogPage{}, changedAuditPage()
		}
		return api.PolicyLogPage{}, auditReadError()
	}
	currentID, err := auditPageFileID(current)
	if err != nil || currentID != identity || after.Size() < before.Size() || current.Size() < before.Size() {
		return api.PolicyLogPage{}, changedAuditPage()
	}
	if err := ctx.Err(); err != nil {
		return api.PolicyLogPage{}, err
	}
	next.Size = max(after.Size(), current.Size())
	return finishAuditPage(page, next)
}

func finishAuditPage(page api.PolicyLogPage, cursor auditPageCursor) (api.PolicyLogPage, error) {
	encoded, err := encodeAuditPageCursor(cursor)
	if err != nil {
		return api.PolicyLogPage{}, err
	}
	page.Cursor = encoded
	return page, nil
}

func scanAuditPage(ctx context.Context, reader *bufio.Reader, cursor auditPageCursor, query auditPageQuery, snapshotSize int64) (api.PolicyLogPage, auditPageCursor, error) {
	page := api.PolicyLogPage{Entries: []api.NetAuditEntry{}}
	scanned, encodedBytes := 0, 0
	for {
		line, err := readAuditPageLine(ctx, reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// A partial final line has no committed position or record.
				return page, cursor, nil
			}
			return api.PolicyLogPage{}, cursor, err
		}
		if scanned != 0 && len(line) > api.PolicyLogMaxDataBytes-scanned {
			page.More = true
			return page, cursor, nil
		}
		var entry api.NetAuditEntry
		if json.Unmarshal(line, &entry) != nil {
			return api.PolicyLogPage{}, cursor, auditReadError()
		}
		if entry.Kind == "net" && auditEntryMatches(entry, query.cutoff, query.by) {
			data, err := json.Marshal(entry) //nolint:gosec // G117: Secret is a public name, never a secret value.
			if err != nil {
				return api.PolicyLogPage{}, cursor, auditReadError()
			}
			if len(data)+1 > api.PolicyLogMaxDataBytes {
				return api.PolicyLogPage{}, cursor, UserError("audit_too_large", "one network audit entry is too large for a page", "Check the session's audit log for an oversized record.")
			}
			if len(data)+1 > api.PolicyLogMaxDataBytes-encodedBytes {
				page.More = true
				return page, cursor, nil
			}
			encodedBytes += len(data) + 1
			page.Entries = append(page.Entries, entry)
		}
		scanned += len(line)
		cursor.Offset += int64(len(line))
		if len(page.Entries) == query.limit || scanned >= api.PolicyLogMaxDataBytes {
			page.More = cursor.Offset < snapshotSize
			return page, cursor, nil
		}
	}
}

func readAuditPageLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := reader.ReadSlice('\n')
		if len(part) > maxAuditLineBytes-len(line) {
			return nil, auditReadError()
		}
		line = append(line, part...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil, io.EOF
		default:
			return nil, auditPageReadError(ctx)
		}
	}
}

func auditPageReadError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return auditReadError()
}
