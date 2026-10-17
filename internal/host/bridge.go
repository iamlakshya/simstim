package host

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Features this host helper offers. Clients check them before using a feature.
var Features = []string{"attach.replay", "upload", "peek", "setup", "rename", "send", "hooks", "restore", "ports", "worktree", "folders", "alerts", "trace"}

// MaxUpload is the largest file a client may upload.
const MaxUpload = 16 << 20

type upload struct {
	session string
	name    string
	size    int64
	f       *os.File
	n       int64
}

type bridge struct {
	w *proto.Writer
	// log takes the bridge's debug lines, while its debug is on
	// (bridge_debug.go).
	log *slog.Logger
	dbg bridgeDebug
	bun bundleFiles // what debug.collect listed (bridge_collect.go)

	// traced is true while the attach asked for a debug trace of its keys.
	// Only the loop in RunBridge uses it.
	traced bool

	mu      sync.Mutex
	holder  net.Conn
	hw      *proto.Writer
	uploads map[uint64]*upload
	hashes  map[uint64]interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
}

// RunBridge serves one client over in and out (the SSH channel's stdio).
func RunBridge(in io.Reader, out io.Writer) error {
	r := bufio.NewReaderSize(in, 256<<10)
	w := proto.NewWriter(out)
	f, err := proto.Read(r)
	if err != nil {
		return err
	}
	if f.Type != proto.THello {
		return errors.New("expected hello")
	}
	var hello proto.Hello
	if err := json.Unmarshal(f.Payload, &hello); err != nil {
		return fmt.Errorf("read the client's hello: %w", err)
	}
	if err := hello.Check(version.Protocol); err != nil {
		_ = w.WriteJSON(proto.TError, proto.Error{Code: "protocol", Message: err.Error(), Hint: "Update the client."})
		return err
	}
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge", Features: Features}); err != nil {
		return err
	}
	lines := diag.NewLines()
	b := &bridge{w: w, log: slog.New(lines), dbg: bridgeDebug{lines: lines}, uploads: map[uint64]*upload{}, hashes: map[uint64]interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}{}}
	liveBridges.Store(b, true)
	defer liveBridges.Delete(b)
	defer b.stopDebug("end")
	defer b.closeHolder()
	for {
		f, err := proto.Read(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch f.Type {
		case proto.TPing:
			_ = w.Write(proto.TPong, proto.Pong(f.Payload, diag.Now()))
		case proto.TRequest:
			var req proto.Request
			if json.Unmarshal(f.Payload, &req) == nil {
				go b.serve(req)
			}
		case proto.TAttach:
			var a proto.Attach
			if json.Unmarshal(f.Payload, &a) == nil {
				b.note("attach", 0)
				if err := b.attach(a); err != nil {
					_ = w.WriteJSON(proto.TError, proto.Error{Code: "attach_failed", Message: err.Error(), Hint: "List sessions; it may have been deleted."})
				}
			}
		case proto.TInput:
			if b.traced {
				// When the bridge read the key, for the holder's trace.
				b.forward(proto.Frame{Type: proto.TTrace, Payload: proto.EncodeTime(diag.Now())})
			}
			b.forward(f)
		case proto.TResize:
			b.forward(f)
		case proto.TDetach:
			b.note("detach", 0)
			b.forward(f)
			b.closeHolder()
		case proto.TUploadBegin:
			b.uploadBegin(f.Payload)
		case proto.TUploadChunk:
			b.uploadChunk(f.Payload)
		case proto.TUploadEnd:
			b.uploadEnd(f.Payload)
		default:
			// A client sends no other frame: drop it.
		}
	}
}

func (b *bridge) reply(id uint64, v any, err error) {
	b.noteReply(id)
	resp := proto.Response{ID: id, OK: err == nil}
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			resp.Error = pe
		} else {
			resp.Error = &proto.Error{Code: "error", Message: err.Error()}
		}
	} else if v != nil {
		resp.Result, _ = json.Marshal(v)
	}
	_ = b.w.WriteJSON(proto.TResponse, resp)
}

func (b *bridge) handle(req proto.Request) {
	switch req.Op {
	case "ls":
		v, err := List()
		b.reply(req.ID, v, err)
	case "new":
		var a NewArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		v, err := New(a)
		b.reply(req.ID, v, err)
	case "delete":
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Args, &a)
		res, err := Delete(a.ID)
		b.reply(req.ID, res, err)
	case "rename":
		var a struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, map[string]string{"name": a.Name}, Rename(a.ID, a.Name))
	case "peek":
		var a struct {
			ID    string `json:"id"`
			Bytes int    `json:"bytes"`
		}
		_ = json.Unmarshal(req.Args, &a)
		if !validID(a.ID) {
			b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, ""))
			return
		}
		res, err := HolderRequest(a.ID, "peek", map[string]int{"bytes": a.Bytes}, 3*time.Second)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		b.reply(req.ID, res, nil)
	case "send":
		var a struct {
			ID    string `json:"id"`
			Text  string `json:"text"`
			Enter bool   `json:"enter"`
		}
		_ = json.Unmarshal(req.Args, &a)
		if !validID(a.ID) {
			b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, ""))
			return
		}
		res, err := HolderRequest(a.ID, "send", map[string]any{"text": a.Text, "enter": a.Enter}, 3*time.Second)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		b.reply(req.ID, res, nil)
	case "ports":
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Args, &a)
		if !validID(a.ID) {
			b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, ""))
			return
		}
		res, err := HolderRequest(a.ID, "ports", nil, 3*time.Second)
		if err != nil {
			b.reply(req.ID, nil, err)
			return
		}
		b.reply(req.ID, res, nil)
	case "folders":
		var a struct {
			Prefix string `json:"prefix"`
			Limit  int    `json:"limit"`
		}
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, Folders(a.Prefix, a.Limit), nil)
	case "restore":
		var a struct {
			All bool `json:"all"`
		}
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, Restore(a.All), nil)
	case "doctor":
		b.reply(req.ID, Doctor(), nil)
	case "setup":
		var a SetupArgs
		_ = json.Unmarshal(req.Args, &a)
		b.reply(req.ID, Setup(a), nil)
	case "alerts.get", "alerts.set", "alerts.test", "alerts.log", "alerts.off":
		v, err := alertsRequest(req.Op, req.Args)
		b.reply(req.ID, v, err)
	default:
		if !b.handleMore(req) && !b.handleDebug(req) {
			b.reply(req.ID, nil, UserError("unknown_op", "unknown request "+req.Op, "Update the host helper."))
		}
	}
}

func (b *bridge) attach(a proto.Attach) error {
	b.closeHolder()
	b.traced = a.Trace != "" && (len(a.Categories) == 0 || slices.Contains(a.Categories, diag.CatAttach))
	if !validID(a.Session) {
		return fmt.Errorf("no session %s", a.Session)
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(context.Background(), "unix", paths.Socket(a.Session))
	if err != nil {
		return fmt.Errorf("session %s is not running", a.Session)
	}
	hw := proto.NewWriter(conn)
	hr := bufio.NewReaderSize(conn, 256<<10)
	if err := hw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		conn.Close()
		return err
	}
	f, err := proto.Read(hr)
	if err != nil || f.Type != proto.THello {
		conn.Close()
		return errors.New("the session's holder did not answer")
	}
	if err := hw.WriteJSON(proto.TAttach, a); err != nil {
		conn.Close()
		return err
	}
	b.mu.Lock()
	b.holder, b.hw = conn, hw
	b.mu.Unlock()
	go func() {
		for {
			f, err := proto.Read(hr)
			if err != nil {
				b.mu.Lock()
				mine := b.holder == conn
				if mine {
					b.holder, b.hw = nil, nil
				}
				b.mu.Unlock()
				conn.Close()
				if mine {
					_ = b.w.WriteJSON(proto.TError, proto.Error{Code: "session_gone", Message: "the session stopped"})
				}
				return
			}
			switch f.Type {
			case proto.TAttached, proto.TOutput, proto.TSnapshot, proto.TEnded, proto.TError:
				if err := b.w.Write(f.Type, f.Payload); err != nil {
					conn.Close()
					return
				}
			default:
				// Other frames from the holder are not for the client.
			}
		}
	}()
	return nil
}

func (b *bridge) forward(f proto.Frame) {
	b.mu.Lock()
	hw := b.hw
	b.mu.Unlock()
	if hw != nil {
		_ = hw.Write(f.Type, f.Payload)
	}
}

func (b *bridge) closeHolder() {
	b.mu.Lock()
	c := b.holder
	b.holder, b.hw = nil, nil
	b.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func safeName(n string) string {
	n = filepath.Base(n)
	var sb strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	s := strings.TrimLeft(sb.String(), ".")
	if s == "" {
		s = "file"
	}
	if len(s) > 80 {
		s = s[len(s)-80:]
	}
	return s
}

func (b *bridge) uploadBegin(p []byte) {
	var u proto.UploadBegin
	if err := json.Unmarshal(p, &u); err != nil {
		return
	}
	fail := func(err error) { b.reply(u.ID, nil, err) }
	if !validID(u.Session) {
		fail(UserError("not_found", "no session "+u.Session, ""))
		return
	}
	if u.Size < 0 || u.Size > MaxUpload {
		fail(UserError("too_big", "the file is larger than 16 MiB", ""))
		return
	}
	dir := paths.PasteDir(u.Session)
	if err := paths.EnsurePrivateDir(dir); err != nil {
		fail(err)
		return
	}
	cleanOld(dir, 24*time.Hour)
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		fail(err)
		return
	}
	b.mu.Lock()
	b.uploads[u.ID] = &upload{session: u.Session, name: safeName(u.Name), size: u.Size, f: f}
	b.hashes[u.ID] = sha256.New()
	b.mu.Unlock()
}

func (b *bridge) uploadChunk(p []byte) {
	id, data, err := proto.DecodeChunk(p)
	if err != nil {
		return
	}
	b.mu.Lock()
	u := b.uploads[id]
	h := b.hashes[id]
	b.mu.Unlock()
	if u == nil {
		return
	}
	u.n += int64(len(data))
	if u.n > MaxUpload {
		return
	}
	_, _ = u.f.Write(data)
	_, _ = h.Write(data)
}

func (b *bridge) uploadEnd(p []byte) {
	var e proto.UploadEnd
	if err := json.Unmarshal(p, &e); err != nil {
		return
	}
	b.mu.Lock()
	u := b.uploads[e.ID]
	h := b.hashes[e.ID]
	delete(b.uploads, e.ID)
	delete(b.hashes, e.ID)
	b.mu.Unlock()
	if u == nil {
		b.reply(e.ID, nil, UserError("upload", "unknown upload", ""))
		return
	}
	u.f.Close()
	sum := hex.EncodeToString(h.Sum(nil))
	if u.n != u.size || !strings.EqualFold(sum, e.SHA256) {
		os.Remove(u.f.Name())
		b.reply(e.ID, nil, UserError("upload", "the upload was damaged in transit", "Try again."))
		return
	}
	final := filepath.Join(filepath.Dir(u.f.Name()), sum[:12]+"-"+u.name)
	if err := os.Rename(u.f.Name(), final); err != nil {
		b.reply(e.ID, nil, err)
		return
	}
	_ = os.Chmod(final, 0o600)
	b.note("upload", int(u.n))
	b.reply(e.ID, proto.UploadResult{Path: final}, nil)
}

func cleanOld(dir string, age time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > age {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
