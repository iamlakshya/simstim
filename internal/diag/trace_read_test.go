package diag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceReadKeepsTheActualWriterAfterPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	file, err := Open(path, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	file.Add(Record{Ev: "key", Trace: "original", Key: 1})
	first, err := file.ReadCurrentV1(t.Context(), "", 0)
	if err != nil || !strings.Contains(string(first.Data), "original") {
		t.Fatalf("original writer was not flushed: %s, %v", first.Data, err)
	}
	if err := os.Rename(path, path+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("FOREIGN PATH"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := file.ReadCurrentV1(t.Context(), first.Epoch, 0)
	if err != nil || strings.Contains(string(got.Data), "FOREIGN") || !strings.Contains(string(got.Data), "original") {
		t.Fatalf("read adopted a replacement pathname: %s, %v", got.Data, err)
	}
}

func TestTraceReadCursorCannotCrossWriterOrTurnover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	file, err := Open(path, 128)
	if err != nil {
		t.Fatal(err)
	}
	file.Add(Record{Ev: "key", Trace: "one", Key: 1})
	old, err := file.ReadCurrentV1(t.Context(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	file.Add(Record{Ev: "log", Text: strings.Repeat("x", 256)})
	if _, err := file.ReadCurrentV1(t.Context(), old.Epoch, 0); err == nil {
		t.Fatal("a cursor crossed native writer turnover")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, err := replacement.ReadCurrentV1(t.Context(), old.Epoch, 0); err == nil {
		t.Fatal("a cursor selected another writer")
	}
}

func TestTraceReadIsBoundedAndJoinsCancellation(t *testing.T) {
	file, err := Open(filepath.Join(t.TempDir(), "trace.jsonl"), 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for range 8 {
		file.Add(Record{Ev: "log", Text: strings.Repeat("x", 200000)})
	}
	chunk, err := file.ReadCurrentV1(t.Context(), "", 0)
	if err != nil || len(chunk.Data) != TraceReadMaxV1 || chunk.EOF || chunk.Next != TraceReadMaxV1 {
		t.Fatalf("unbounded or incomplete original read: %d, %v", len(chunk.Data), err)
	}
	clock, err := file.ReadClockV1(t.Context())
	if err != nil || clock.Now <= 0 || len(clock.Data) != 0 || clock.Epoch != chunk.Epoch {
		t.Fatalf("clock calibration replaced its original writer: %+v, %v", clock, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := file.ReadCurrentV1(ctx, chunk.Epoch, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read ran: %v", err)
	}
}
