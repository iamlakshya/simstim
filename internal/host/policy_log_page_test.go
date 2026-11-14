package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

func auditPageRecord(t *testing.T, host string) string {
	t.Helper()
	data, err := json.Marshal(api.NetAuditEntry{V: 1, T: "2026-11-13T12:00:00Z", Kind: "net", Host: host, Decision: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func auditPageFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuditPagesPreserveIdenticalRecordsAndLegacyArray(t *testing.T) {
	line := auditPageRecord(t, "same.example.com")
	path := auditPageFile(t, strings.Repeat(line, 3))
	args := api.PolicyLogArgs{ID: "page2345", Page: true, Limit: 1}
	var all []api.NetAuditEntry
	last := int64(0)
	for i := range 3 {
		page, err := ReadAuditNetPage(context.Background(), path, args)
		if err != nil || len(page.Entries) != 1 || page.More != (i != 2) || page.Cursor == "" {
			t.Fatalf("page%d = %+v, %v", i, page, err)
		}
		cursor, err := decodeAuditPageCursor(page.Cursor)
		if err != nil || cursor.Offset != last+int64(len(line)) {
			t.Fatalf("equal record did not advance its own source offset: %+v, %v", cursor, err)
		}
		last = cursor.Offset
		args.Cursor = page.Cursor
		all = append(all, page.Entries...)
	}
	if len(all) != 3 || !reflect.DeepEqual(all[0], all[1]) || !reflect.DeepEqual(all[1], all[2]) {
		t.Fatal("equal entries were deduplicated or changed")
	}
	legacy, err := ReadPolicyLog(path, api.PolicyLogArgs{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(legacy)
	if err != nil || len(data) == 0 || data[0] != '[' {
		t.Fatalf("legacy snapshot became an envelope: %s, %v", data, err)
	}
}

func TestAuditPagesWaitAtAnIncompleteFinalLine(t *testing.T) {
	line := auditPageRecord(t, "partial.example.com")
	path := auditPageFile(t, strings.TrimSuffix(line, "\n"))
	args := api.PolicyLogArgs{ID: "page2345", Page: true}
	page, err := ReadAuditNetPage(context.Background(), path, args)
	if err != nil || len(page.Entries) != 0 || page.More {
		t.Fatalf("partial line was emitted: %+v, %v", page, err)
	}
	cursor, err := decodeAuditPageCursor(page.Cursor)
	if err != nil || cursor.Offset != 0 {
		t.Fatalf("partial line was committed: %+v, %v", cursor, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, "\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	args.Cursor = page.Cursor
	page, err = ReadAuditNetPage(context.Background(), path, args)
	if err != nil || len(page.Entries) != 1 || page.More {
		t.Fatalf("completed line was lost or duplicated: %+v, %v", page, err)
	}
}

func TestAuditPagesAdvanceAcrossFilteredRecordsWithABoundedScan(t *testing.T) {
	noise := "{\"kind\":\"event\",\"data\":\"" + strings.Repeat("x", 128) + "\"}\n"
	path := auditPageFile(t, strings.Repeat(noise, (api.PolicyLogMaxDataBytes/len(noise))+100)+auditPageRecord(t, "last.example.com"))
	args := api.PolicyLogArgs{ID: "page2345", Page: true}
	first, err := ReadAuditNetPage(context.Background(), path, args)
	if err != nil || len(first.Entries) != 0 || !first.More {
		t.Fatalf("filtered page did not stop at its budget: %+v, %v", first, err)
	}
	cursor, err := decodeAuditPageCursor(first.Cursor)
	if err != nil || cursor.Offset <= 0 || cursor.Offset > 2*api.PolicyLogMaxDataBytes {
		t.Fatalf("filtered records lost bounded progress: %+v, %v", cursor, err)
	}
	args.Cursor = first.Cursor
	next, err := ReadAuditNetPage(context.Background(), path, args)
	if err != nil || len(next.Entries) != 1 || next.Entries[0].Host != "last.example.com" || next.More {
		t.Fatalf("matching record after filtered budget was lost: %+v, %v", next, err)
	}
}

func TestAuditPageMissingFilePinsTheOriginalCutoffAndFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	args := api.PolicyLogArgs{ID: "page2345", Page: true, Since: "1h"}
	first, err := ReadAuditNetPage(context.Background(), path, args)
	if err != nil || first.Cursor == "" || first.More || len(first.Entries) != 0 {
		t.Fatalf("missing initial file failed: %+v, %v", first, err)
	}
	args.Cursor = first.Cursor
	query, original, err := prepareAuditPage(args, time.Now().Add(2*time.Hour))
	if err != nil || original.Cutoff == "" || query.cutoff.UTC().Format(time.RFC3339Nano) != original.Cutoff {
		t.Fatalf("duration cutoff moved on continuation: %+v %+v %v", query, original, err)
	}
	again, err := ReadAuditNetPage(context.Background(), path, args)
	if err != nil || again.Cursor != first.Cursor {
		t.Fatalf("missing-file progress was reset: %+v, %v", again, err)
	}
	created, err := json.Marshal(api.NetAuditEntry{V: 1, T: time.Now().UTC().Format(time.RFC3339), Kind: "net", Host: "created.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(created, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	appeared, err := ReadAuditNetPage(context.Background(), path, args)
	if err != nil || len(appeared.Entries) != 1 || appeared.Entries[0].Host != "created.example.com" {
		t.Fatalf("creation after a waiting cursor lost history: %+v, %v", appeared, err)
	}
	args.Cursor = appeared.Cursor
	args.Since = ""
	if _, err := ReadAuditNetPage(context.Background(), path, args); err == nil {
		t.Fatal("a continuation changed filters silently")
	}
}

func TestAuditPagesRejectObservableReplacementRemovalAndShrinking(t *testing.T) {
	for _, action := range []string{"replace", "remove", "truncate-unread-tail"} {
		t.Run(action, func(t *testing.T) {
			line := auditPageRecord(t, "entry.example.com")
			path := auditPageFile(t, strings.Repeat(line, 3))
			args := api.PolicyLogArgs{ID: "page2345", Page: true, Limit: 1}
			first, err := ReadAuditNetPage(context.Background(), path, args)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "replace":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Repeat(line, 3)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "remove":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "truncate-unread-tail":
				if err := os.Truncate(path, int64(2*len(line))); err != nil {
					t.Fatal(err)
				}
			}
			args.Cursor = first.Cursor
			page, err := ReadAuditNetPage(context.Background(), path, args)
			policyError(t, err, "audit_changed")
			if page.Cursor != "" || len(page.Entries) != 0 {
				t.Fatal("a changed source returned successful partial progress")
			}
		})
	}
}

func TestAuditPageUnsupportedRewritesNeverCauseAnAutomaticReplay(t *testing.T) {
	before := auditPageRecord(t, "oldx.example.com")
	after := auditPageRecord(t, "newx.example.com")
	path := auditPageFile(t, before)
	args := api.PolicyLogArgs{ID: "page2345", Page: true}
	first, err := ReadAuditNetPage(context.Background(), path, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("rewrite fixture must keep its byte size")
	}
	if err := os.WriteFile(path, []byte(after), 0o600); err != nil {
		t.Fatal(err)
	}
	args.Cursor = first.Cursor
	page, err := ReadAuditNetPage(context.Background(), path, args)
	// In-place hostile rewriting is outside the append-only contract.
	// Detection is optional; automatic replay or reset is never permitted.
	if err != nil {
		policyError(t, err, "audit_changed")
		return
	}
	if len(page.Entries) != 0 || page.More {
		t.Fatalf("unsupported rewrite replayed already emitted data: %+v", page)
	}
}

func TestAuditPageErrorsReturnNoPartialSuccessfulPage(t *testing.T) {
	for _, suffix := range []string{"not-json\n", strings.Repeat("x", maxAuditLineBytes+1)} {
		path := auditPageFile(t, auditPageRecord(t, "first.example.com")+suffix)
		page, err := ReadAuditNetPage(context.Background(), path, api.PolicyLogArgs{ID: "page2345", Page: true})
		policyError(t, err, "audit_read")
		if page.Cursor != "" || len(page.Entries) != 0 {
			t.Fatal("read failure returned partial successful history")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, err := ReadAuditNetPage(ctx, filepath.Join(t.TempDir(), "missing"), api.PolicyLogArgs{ID: "page2345", Page: true})
	if !errors.Is(err, context.Canceled) || page.Cursor != "" {
		t.Fatalf("cancellation became progress: %+v %v", page, err)
	}
}

type failedPageReader struct{ used bool }

func (reader *failedPageReader) Read(p []byte) (int, error) {
	if !reader.used {
		reader.used = true
		return copy(p, "{\"kind\":\"event\"}\n"), nil
	}
	return 0, errors.New("fixture read failure")
}

func TestAuditPageInjectedReadErrorAndMalformedCursorAreTyped(t *testing.T) {
	_, _, err := scanAuditPage(context.Background(), bufio.NewReader(&failedPageReader{}), auditPageCursor{}, auditPageQuery{limit: 1}, 100)
	policyError(t, err, "audit_read")
	for _, args := range []api.PolicyLogArgs{
		{ID: "page2345", Page: true, Cursor: "not^base64"},
		{ID: "page2345", Page: true, Cursor: strings.Repeat("a", api.PolicyLogMaxCursorBytes+1)},
		{ID: "page2345", Page: true, Limit: -1},
		{ID: "page2345", Page: true, Limit: api.PolicyLogMaxLimit + 1},
		{ID: "page2345", Page: true, Max: 1},
		{ID: "page2345", Page: true, Door: "mount"},
	} {
		_, err := ReadAuditNetPage(context.Background(), filepath.Join(t.TempDir(), "missing"), args)
		policyError(t, err, "bad_request")
	}
}

func TestAuditPageRejectsAnOversizedEncodedEntryAndWritableFile(t *testing.T) {
	// Raw JSON fits the line bound, while canonical JSON escaping would
	// exceed the response bound. The record must fail, never loop or skip.
	path := auditPageFile(t, "{\"kind\":\"net\",\"host\":\""+strings.Repeat("<", api.PolicyLogMaxDataBytes/2)+"\"}\n")
	page, err := ReadAuditNetPage(context.Background(), path, api.PolicyLogArgs{ID: "page2345", Page: true})
	policyError(t, err, "audit_too_large")
	if page.Cursor != "" || len(page.Entries) != 0 {
		t.Fatal("oversized response committed or emitted partial data")
	}
	path = auditPageFile(t, auditPageRecord(t, "entry.example.com"))
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	_, err = ReadAuditNetPage(context.Background(), path, api.PolicyLogArgs{ID: "page2345", Page: true})
	policyError(t, err, "audit_read")
}

func TestPagedBridgeReadDoesNotForwardToAnOlderLiveSupervisor(t *testing.T) {
	m := savedPolicyManifest(t)
	if err := os.WriteFile(paths.AuditLog(m.ID), []byte(auditPageRecord(t, "entry.example.com")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(paths.Socket(m.ID))); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.Socket(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var contacted atomic.Int32
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			contacted.Add(1)
			_ = conn.Close()
		}
	}()
	defer func() { _ = listener.Close(); <-done }()
	args, err := json.Marshal(api.PolicyLogArgs{ID: m.ID, Page: true})
	if err != nil {
		t.Fatal(err)
	}
	value, err := dispatchPolicyOp("policy.log", args)
	page, ok := value.(api.PolicyLogPage)
	if err != nil || !ok || len(page.Entries) != 1 || contacted.Load() != 0 {
		t.Fatalf("new page depended on an older session protocol: value=%+v error=%v contacts=%d", value, err, contacted.Load())
	}
}
