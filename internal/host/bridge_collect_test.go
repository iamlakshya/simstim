package host_test

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

// A debug folder holds what the debug mode wrote, but anyone who can write
// in it can put something else there. debug.collect lists only the names
// that the debug mode gives its files, and debug.read sends no link: a
// key linked in as a saved window stays on the host.
func TestTheBridgeSendsNoLinkAndNoOtherFile(t *testing.T) {
	h := hosttest.New(t)
	id := "linkedz2"
	dir := paths.SessionDebugDir(id)
	if err := diag.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(h.Home, "id_ed25519")
	if err := os.WriteFile(key, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519"), []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(key, filepath.Join(dir, "flight-20261016-000001.trace")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(key, filepath.Join(dir, "heap-20261016-000002.pprof")); err != nil {
		t.Fatal(err)
	}
	b := newRawBridge(t)
	col := decode[api.DebugCollected](t, b.request(1, api.OpDebugCollect, api.DebugCollectArgs{IDs: []string{id}}))
	prefix := "host/sessions/" + id + "/debug/"
	for _, f := range col.Files {
		if f.Path == prefix+"id_ed25519" || f.Path == prefix+"heap-20261016-000002.pprof" {
			t.Errorf("collect listed %s", f.Path)
		}
	}
	r := b.request(2, api.OpDebugRead, api.DebugReadArgs{Name: prefix + "flight-20261016-000001.trace"})
	if r.OK {
		t.Fatalf("debug.read sent a hard link: %s", r.Result)
	}
}

// debug.collect lists a session's files by their names in the bundle, with
// fresh profiles. debug.read sends a listed file in parts that end at a
// line's end, with the alerts file's values replaced, and nothing else.
func TestTheBridgeCollectsAndReads(t *testing.T) {
	h := hosttest.New(t)
	const token = "tk_turnip_31337"
	if err := alerts.Save(paths.AlertsConfig(), api.AlertsConfig{Destinations: []api.AlertDestination{
		{Type: "ntfy", URL: "https://ntfy.example.com/topic", Token: token}}}); err != nil {
		t.Fatal(err)
	}
	s, err := host.New(host.NewArgs{Name: "coll", Cwd: "~", Argv: []string{"/bin/sh", "-c", "exec sleep 60"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	// 1.5 MiB of lines, each with the token, so that a part ends inside
	// one unless it ends at a line's end.
	var want, file bytes.Buffer
	line := strings.Repeat("x", 977) + " token=" + token + "\n"
	for range 1500 {
		file.WriteString(line)
		want.WriteString(strings.ReplaceAll(line, token, alerts.Hidden))
	}
	if err := diag.WriteFile(paths.DebugTrace(s.ID), file.Bytes()); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	_, _ = z.Write([]byte("label " + token))
	_ = z.Close()
	if err := diag.WriteFile(filepath.Join(paths.SessionDebugDir(s.ID), "cpu-20261016-000000.pprof"), gz.Bytes()); err != nil {
		t.Fatal(err)
	}

	b := newRawBridge(t)
	col := decode[api.DebugCollected](t, b.request(1, api.OpDebugCollect, api.DebugCollectArgs{IDs: []string{s.ID, "nosuchid"}}))
	prefix := "host/sessions/" + s.ID + "/"
	var listed []string
	for _, f := range col.Files {
		listed = append(listed, f.Path)
	}
	if col.Home != h.Home || !slices.Contains(listed, prefix+"debug.jsonl") || !slices.Contains(listed, prefix+"holder.log") ||
		!slices.ContainsFunc(listed, func(p string) bool { return strings.HasPrefix(p, prefix+"debug/heap-") }) ||
		!slices.ContainsFunc(listed, func(p string) bool { return strings.HasPrefix(p, prefix+"debug/goroutine-") }) {
		t.Fatalf("collected %+v", col)
	}
	if slices.Contains(listed, prefix+"debug/cpu-20261016-000000.pprof") ||
		!slices.ContainsFunc(col.Notes, func(n string) bool { return strings.Contains(n, "cpu-20261016-000000.pprof: left out") }) ||
		!slices.ContainsFunc(col.Notes, func(n string) bool { return strings.Contains(n, "no session nosuchid") }) {
		t.Fatalf("notes %v, files %v", col.Notes, listed)
	}

	var got bytes.Buffer
	hidden, parts := 0, 0
	for off := int64(0); ; parts++ {
		ch := decode[api.DebugChunk](t, b.request(uint64(10+parts), api.OpDebugRead, api.DebugReadArgs{Name: prefix + "debug.jsonl", Offset: off}))
		if len(ch.Data) > api.DebugReadMax || len(ch.Data) > 0 && ch.Data[len(ch.Data)-1] != '\n' {
			t.Fatalf("part %d has %d bytes, and ends in %q", parts, len(ch.Data), ch.Data[len(ch.Data)-1:])
		}
		got.Write(ch.Data)
		hidden += ch.Hidden
		if ch.EOF {
			break
		}
		off = ch.Next
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) || hidden != 1500 || parts < 1 {
		t.Fatalf("read %d bytes in %d parts, %d hidden; want %d bytes", got.Len(), parts+1, hidden, want.Len())
	}
	for i, name := range []string{"debug.jsonl", "../../alerts.toml", paths.AlertsConfig()} {
		if r := b.request(uint64(100+i), api.OpDebugRead, api.DebugReadArgs{Name: name}); r.OK || r.Error.Code != "not_found" {
			t.Errorf("a read of %q = %+v", name, r)
		}
	}
	all := decode[api.DebugCollected](t, b.request(200, api.OpDebugCollect, api.DebugCollectArgs{All: true}))
	if !slices.ContainsFunc(all.Files, func(f api.DebugFile) bool { return f.Path == prefix+"debug.jsonl" }) {
		t.Fatalf("a collect of every session misses %s: %+v", s.ID, all.Files)
	}
}
