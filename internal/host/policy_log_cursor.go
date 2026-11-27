package host

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type auditPageCursor struct {
	Version int    `json:"v"`
	Query   string `json:"q"`
	Cutoff  string `json:"c"`
	File    string `json:"f"`
	Offset  int64  `json:"o"`
	Size    int64  `json:"s"`
}

type auditPageQuery struct {
	by     string
	cutoff time.Time
	limit  int
}

func prepareAuditPage(a api.PolicyLogArgs, now time.Time) (auditPageQuery, auditPageCursor, error) {
	limit := a.Limit
	if limit == 0 {
		limit = api.PolicyLogDefaultLimit
	}
	door := strings.TrimSpace(string(a.Door))
	by, since := strings.TrimSpace(a.By), strings.TrimSpace(a.Since)
	if a.ID == "" || len(a.ID) > 128 || a.Max != 0 || limit < 1 || limit > api.PolicyLogMaxLimit ||
		(door != "" && door != string(api.DoorNet)) || len(by) > 256 || len(since) > 128 ||
		api.CleanText(by, len(by)) != by {
		return auditPageQuery{}, auditPageCursor{}, badAuditPageArgs()
	}
	cutoff, err := auditCutoff(since, now)
	if err != nil {
		return auditPageQuery{}, auditPageCursor{}, err
	}
	binding, err := json.Marshal([]string{a.ID, string(api.DoorNet), by, since})
	if err != nil {
		return auditPageQuery{}, auditPageCursor{}, badAuditPageArgs()
	}
	sum := sha256.Sum256(binding)
	cursor := auditPageCursor{Version: 1, Query: hex.EncodeToString(sum[:])}
	if since != "" {
		cursor.Cutoff = cutoff.UTC().Format(time.RFC3339Nano)
	}
	if a.Cursor != "" {
		decoded, err := decodeAuditPageCursor(a.Cursor)
		if err != nil {
			return auditPageQuery{}, auditPageCursor{}, err
		}
		if decoded.Query != cursor.Query || (since == "") != (decoded.Cutoff == "") {
			return auditPageQuery{}, auditPageCursor{}, badAuditPageArgs()
		}
		if decoded.Cutoff != "" {
			cutoff, err = time.Parse(time.RFC3339Nano, decoded.Cutoff)
			if err != nil {
				return auditPageQuery{}, auditPageCursor{}, badAuditPageArgs()
			}
		}
		cursor = decoded
	}
	return auditPageQuery{by: by, cutoff: cutoff, limit: limit}, cursor, nil
}

func decodeAuditPageCursor(raw string) (auditPageCursor, error) {
	var cursor auditPageCursor
	if len(raw) > api.PolicyLogMaxCursorBytes {
		return cursor, badAuditPageArgs()
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, badAuditPageArgs()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || cursor.Version != 1 || len(cursor.Query) != 64 ||
		len(cursor.File) > 64 || cursor.Offset < 0 || cursor.Size < cursor.Offset ||
		(cursor.File == "" && (cursor.Offset != 0 || cursor.Size != 0)) {
		return auditPageCursor{}, badAuditPageArgs()
	}
	if cursor.File != "" {
		identity, err := hex.DecodeString(cursor.File)
		if err != nil || len(identity) != sha256.Size {
			return auditPageCursor{}, badAuditPageArgs()
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return auditPageCursor{}, badAuditPageArgs()
	}
	return cursor, nil
}

func encodeAuditPageCursor(cursor auditPageCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", badAuditPageArgs()
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > api.PolicyLogMaxCursorBytes {
		return "", badAuditPageArgs()
	}
	return encoded, nil
}

func auditPageFileID(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || !auditOwnedInfo(info) {
		return "", auditReadError()
	}
	// This encodes only file identity metadata. It is not a content or
	// sampled-prefix hash and makes no in-place rewrite integrity claim.
	identity := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)))
	return hex.EncodeToString(identity[:]), nil
}

func auditOwnedInfo(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid()) && info.Mode().Perm()&0o022 == 0
}

func badAuditPageArgs() error {
	return UserError("bad_request", "invalid policy log page or changed filters", "Start the log read again with the same session and filters.")
}

func changedAuditPage() error {
	return UserError("audit_changed", "the session's audit log was replaced, removed or truncated", "Start a new log read to choose the current file explicitly.")
}
