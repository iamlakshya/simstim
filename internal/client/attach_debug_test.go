//go:build !windows

package client_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// With every category on, the terminal gets the same bytes as without
// debug, byte for byte.
func TestAttachIsByteExactWithEveryCategory(t *testing.T) {
	r := newAttachRun(t)
	var first string
	checkByteExact(t, r, func(id string) { first = id })
	plain := r.term.Output()
	if _, err := host.Delete(first); err != nil {
		t.Fatal(err)
	}
	r.term.Clear()
	r.state = client.AttachState{}
	startDebug(t, diag.AllCats, "")
	checkByteExact(t, r, nil)
	debugged := r.term.Output()
	stopDebug(t)
	if !bytes.Equal(plain, debugged) {
		t.Fatalf("the terminal got other bytes with debug on:\nwithout %q\nwith    %q", tail(plain), tail(debugged))
	}
}

// The canary (decision 0025): with every category on, on both sides, a
// marker in a secret, the environment, the agent's arguments, the keys and
// the output reaches no debug file, nor holder.log.
func TestNoDebugFileHoldsTheCanary(t *testing.T) {
	r := newAttachRun(t)
	const marker = "CANARY-7f3a9c2e"
	t.Setenv("CANARY_ENV", marker)
	keys := filepath.Join(r.h.Home, "keys.bin")
	// The marker is also the script's last argument, as a prompt or a key
	// can be an agent's.
	s, err := host.New(host.NewArgs{Name: "canary", Cwd: "~", Cols: 80, Rows: 24,
		Secrets: map[string]api.Secret{"CANARY_SECRET": marker},
		Argv: []string{"/bin/sh", "-c", `stty raw -echo; printf 'READY %s %s\n' "$CANARY_SECRET" "$CANARY_ENV"; head -c 15 > ` + keys +
			`; printf 'DONE %s\n' "$1"; exec sleep 60`, "canary", marker}})
	if err != nil {
		t.Fatal(err)
	}
	startDebug(t, diag.AllCats, "")
	conn := dialBox(t, r)
	if _, err := conn.DebugStart(context.Background(), s.ID, diag.HostCats, time.Minute); err != nil {
		t.Fatal(err)
	}
	done := r.attach(context.Background(), s.ID, client.DefaultSettings())
	r.term.WaitFor("READY " + marker + " " + marker) // the secret, then the environment
	r.term.Type(marker)
	r.term.WaitFor("DONE " + marker)
	for _, kind := range []string{"heap", "goroutine", "cpu"} {
		if _, err := conn.DebugProfile(context.Background(), s.ID, kind, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.DebugSave(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	r.term.Type(detach)
	if e := r.end(done); e.err != nil {
		t.Fatal(e.err)
	}
	if _, err := conn.DebugStop(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	stopDebug(t)
	hosttest.WaitFor(t, 10*time.Second, "the host's trace to stop", func() bool {
		return strings.Contains(r.h.HolderLog(s.ID), "debug trace off")
	})
	conn.Close()

	var files []string
	for _, dir := range []string{client.DebugDir(), paths.DebugDir(), paths.SessionDir(s.ID)} {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && !strings.HasSuffix(p, "manifest.json") {
				files = append(files, p)
			}
			return nil
		})
	}
	kinds := map[string]bool{}
	for _, p := range files {
		kinds[kindOf(p)] = true
		if bytes.Contains(readPlain(t, p), []byte(marker)) {
			t.Errorf("%s holds the marker", p)
		}
	}
	for _, k := range []string{"client", "bridge", "debug.jsonl", "holder.log", "pprof", "trace"} {
		if !kinds[k] {
			t.Errorf("no %s file to check among %v", k, files)
		}
	}
}

// kindOf names the kind of a debug file, to check that the test saw each.
func kindOf(p string) string {
	base := filepath.Base(p)
	switch {
	case strings.HasSuffix(base, ".pprof"):
		return "pprof"
	case strings.HasSuffix(base, ".trace"):
		return "trace"
	case strings.HasPrefix(base, "client-"), strings.HasPrefix(base, "bridge-"):
		return strings.SplitN(base, "-", 2)[0]
	}
	return base
}

// readPlain reads a file, gunzipped when it is a profile.
func readPlain(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, ".pprof") {
		return raw
	}
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return b
}

func dialBox(t *testing.T, r *attachRun) *client.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, r.ch, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	return conn
}

// A session that started before the helper's update keeps its old holder,
// which answers unknown_op: the client says that debug works once the
// session starts again.
func TestAnOldHolderHasNoDebugMode(t *testing.T) {
	r := newAttachRun(t)
	const id = "oldholdz"
	sock := paths.Socket(id)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go oldHolder(ln)
	conn := dialBox(t, r)
	_, err = conn.DebugStart(context.Background(), id, diag.HostCats, 0)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "started before the helper") || !strings.Contains(ue.Hint, "starts again") {
		t.Fatalf("err = %v", err)
	}
}

// oldHolder answers each request as a holder from before the debug mode.
func oldHolder(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			r, w := bufio.NewReader(c), proto.NewWriter(c)
			for {
				f, err := proto.Read(r)
				if err != nil {
					return
				}
				switch f.Type {
				case proto.THello:
					_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "holder"})
				case proto.TRequest:
					var req proto.Request
					_ = json.Unmarshal(f.Payload, &req)
					_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: "unknown_op", Message: "unknown request " + req.Op}})
				default:
				}
			}
		}()
	}
}
