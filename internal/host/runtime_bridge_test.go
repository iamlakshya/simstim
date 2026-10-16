package host_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBridgeRequests(t *testing.T) {
	h := hosttest.New(t)
	c := dial(t, h)
	ctx := context.Background()
	if !c.HasFeature("upload") || !c.HasFeature("alerts") {
		t.Fatalf("features = %q", c.Features)
	}

	s, err := c.NewSession(ctx, client.NewArgs{Name: "via-bridge", Argv: []string{"/bin/sh"}, Cwd: "~", Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SendText(ctx, s.ID, "echo BRIDGE-$((2+3))", true); err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, "the typed command's output", func() bool {
		data, cols, rows, err := c.Peek(ctx, s.ID, 4096)
		return err == nil && cols == 100 && rows == 30 && bytes.Contains(data, []byte("BRIDGE-5"))
	})
	if err := c.RenameSession(ctx, s.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	hosttest.WaitFor(t, 5*time.Second, "the new name", func() bool {
		list, err := c.Sessions(ctx)
		return err == nil && len(list) == 1 && list[0].Name == "renamed"
	})
	ports, err := c.Ports(ctx, s.ID)
	if err != nil || len(ports) != 0 {
		t.Errorf("a shell listens on no ports: %+v, %v", ports, err)
	}
	if _, err := c.RoundTrip(ctx); err != nil {
		t.Error(err)
	}

	// Requests about a session that is not there fail with a reason.
	for _, op := range []func() error{
		func() error { _, _, _, err := c.Peek(ctx, "../x", 1); return err },
		func() error { return c.SendText(ctx, "../x", "x", false) },
		func() error { _, err := c.Ports(ctx, "../x"); return err },
		func() error { _, err := c.Ports(ctx, "nosuchid"); return err },
		func() error { return c.SendText(ctx, "nosuchid", "x", false) },
		func() error { _, _, _, err := c.Peek(ctx, "nosuchid", 1); return err },
		func() error { return c.DeleteSession(ctx, "nosuchid") },
		func() error { return c.Call(ctx, "no-such-op", nil, nil) },
		func() error { return c.Call(ctx, "new", "not an object", nil) },
	} {
		if err := op(); err == nil {
			t.Error("a request about nothing worked")
		}
	}
	var ue *client.UserError
	if err := c.Call(ctx, "no-such-op", nil, nil); !errors.As(err, &ue) || ue.Hint != "Update the host helper." {
		t.Errorf("unknown op = %v", err)
	}

	res, err := c.Delete(ctx, s.ID)
	if err != nil || !res.Deleted {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if list, err := c.Sessions(ctx); err != nil || len(list) != 0 {
		t.Fatalf("sessions after delete = %+v, %v", list, err)
	}
}

func TestBridgeDoctorAndSetup(t *testing.T) {
	h := hosttest.New(t)
	c := dial(t, h)
	ctx := context.Background()
	d, err := c.Doctor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.CPUs == 0 || d.OS == "" || d.Version == "" || d.Agents["shell"] != "/bin/sh" {
		t.Errorf("doctor = %+v", d)
	}
	var names []string
	for _, ch := range d.Checks {
		names = append(names, ch.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "systemd user manager") || !strings.Contains(got, "runtime folder") {
		t.Errorf("checks = %s", got)
	}

	// Setup installs a terminfo entry the host lacks, with tic, under ~/.terminfo.
	src := "x-hosttest-term|a terminal for tests,\n\tam, cols#80, lines#24, bel=^G, cr=\\r,\n"
	var r host.SetupResult
	if err := c.Call(ctx, "setup", api.SetupArgs{Terminfo: map[string]string{"x-hosttest-term": src}}, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Steps) != 1 || !r.Steps[0].OK || r.Steps[0].Detail != "installed" {
		t.Fatalf("setup = %+v", r.Steps)
	}
	if err := c.Call(ctx, "setup", api.SetupArgs{Terminfo: map[string]string{"x-hosttest-term": src}}, &r); err != nil || r.Steps[0].Detail != "already installed" {
		t.Fatalf("setup again = %+v, %v", r.Steps, err)
	}
	// Linger needs a systemd user manager; the restore unit says why not.
	t.Setenv("TERM", "xterm-256color")
	setup, err := c.Setup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(setup.Steps) != 1 || setup.Steps[0].Name != "restore after restart" || setup.Steps[0].OK || setup.Steps[0].Detail == "" {
		t.Errorf("setup without systemd = %+v", setup.Steps)
	}
}

func TestBridgeUploads(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "paste", "sleep", "60")
	b := newRawBridge(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 40000) // over two chunks
	sum := sha256.Sum256(data)
	upload := func(id uint64, session, name string, size int64, body []byte, sha string) proto.Response {
		t.Helper()
		b.sendJSON(proto.TUploadBegin, proto.UploadBegin{ID: id, Session: session, Name: name, Size: size})
		for off := 0; off < len(body); off += 256 << 10 {
			b.send(proto.TUploadChunk, proto.EncodeChunk(id, body[off:min(off+256<<10, len(body))]))
		}
		b.sendJSON(proto.TUploadEnd, proto.UploadEnd{ID: id, SHA256: sha})
		return b.response(id)
	}

	r := upload(1, s.ID, "../../My Photo.png", int64(len(data)), data, hex.EncodeToString(sum[:]))
	if !r.OK {
		t.Fatalf("upload: %+v", r.Error)
	}
	var res proto.UploadResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(res.Path) != paths.PasteDir(s.ID) || !strings.HasSuffix(res.Path, "-My_Photo.png") {
		t.Errorf("path = %q: the name must be made safe, in the session's paste folder", res.Path)
	}
	if got, _ := os.ReadFile(res.Path); !bytes.Equal(got, data) {
		t.Error("the uploaded file differs")
	}
	if fi, _ := os.Stat(res.Path); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi)
	}

	if r := upload(2, s.ID, "a.txt", 5, []byte("hello"), strings.Repeat("0", 64)); r.OK || r.Error.Code != "upload" {
		t.Errorf("a damaged upload was taken: %+v", r)
	}
	if r := upload(3, s.ID, "a.txt", host.MaxUpload+1, nil, ""); r.OK || r.Error.Code != "too_big" {
		t.Errorf("a file over the limit: %+v", r)
	}
	if r := upload(4, "../x", "a.txt", 1, []byte("x"), ""); r.OK || r.Error.Code != "not_found" {
		t.Errorf("an upload to no session: %+v", r)
	}
	// An end with no beginning.
	b.sendJSON(proto.TUploadEnd, proto.UploadEnd{ID: 99})
	if r := b.response(99); r.OK {
		t.Error("an unknown upload ended well")
	}
	entries, _ := os.ReadDir(paths.PasteDir(s.ID))
	if len(entries) != 1 {
		t.Errorf("the paste folder holds %d files, want only the good upload", len(entries))
	}

	// The client does the same, and gets the path.
	c := dial(t, h)
	p, err := c.Upload(context.Background(), s.ID, "note.txt", []byte("note"))
	if err != nil || !strings.HasSuffix(p, "-note.txt") {
		t.Fatalf("client upload = %q, %v", p, err)
	}
	if _, err := c.Upload(context.Background(), "../x", "note.txt", []byte("note")); err == nil {
		t.Error("a client upload to no session worked")
	}
}

func TestBridgeAlerts(t *testing.T) {
	h := hosttest.New(t)
	c := dial(t, h)
	ctx := context.Background()
	info, err := c.Alerts(ctx)
	if err != nil || info.Exists {
		t.Fatalf("alerts with no file = %+v, %v", info, err)
	}
	out := filepath.Join(h.Home, "alert.out")
	cfg := client.AlertsConfig{Destinations: []client.AlertDestination{{Type: "command", Run: "cat > " + out}}}
	info, err = c.SetAlerts(ctx, cfg, false)
	if err != nil || !info.Exists || info.Error != "" || info.Path != "~/.config/"+filepath.Base(paths.ConfigDir())+"/alerts.toml" {
		t.Fatalf("set = %+v, %v", info, err)
	}
	delay := "30s"
	if info, err = c.SetAlerts(ctx, client.AlertsConfig{Delay: delay}, true); err != nil || info.Config.Delay != delay || len(info.Config.Destinations) != 1 {
		t.Fatalf("add a setting = %+v, %v", info, err)
	}
	res, err := c.TestAlerts(ctx, "")
	if err != nil || len(res) != 1 || !res[0].OK {
		t.Fatalf("test = %+v, %v", res, err)
	}
	if b, _ := os.ReadFile(out); !bytes.Contains(b, []byte(`"test"`)) {
		t.Errorf("the command got %q, want the test event", b)
	}
	if _, err := c.TestAlerts(ctx, "no-such-event"); err == nil {
		t.Error("a test of an unknown event worked")
	}
	if log, err := c.AlertsLog(ctx, 0); err != nil || log == nil {
		t.Errorf("log = %+v, %v", log, err)
	}
	if _, err := c.SetAlerts(ctx, client.AlertsConfig{Destinations: []client.AlertDestination{{Type: "carrier-pigeon"}}}, false); err == nil {
		t.Error("an unknown destination was saved")
	}
	if removed, err := c.AlertsOff(ctx); err != nil || !removed {
		t.Fatalf("off = %v, %v", removed, err)
	}
	if removed, err := c.AlertsOff(ctx); err != nil || removed {
		t.Fatalf("off again = %v, %v", removed, err)
	}
	// A broken file is reported with what to do.
	writeFile(t, paths.AlertsConfig(), "nonsense = [")
	if info, err := c.Alerts(ctx); err != nil || info.Error == "" || info.Hint == "" {
		t.Fatalf("a broken file = %+v, %v", info, err)
	}
	if _, err := c.SetAlerts(ctx, client.AlertsConfig{Delay: delay}, true); err == nil {
		t.Error("an add to a broken file worked")
	}
	if err := c.Call(ctx, "alerts.set", "not an object", nil); err == nil {
		t.Error("a broken request was taken")
	}
}

// The test transport kills a bridge when the context given to Start ends, as
// client.Transport asks and as the SSH transport does.
func TestTransportStartEndsWithItsContext(t *testing.T) {
	h := hosttest.New(t)
	_, tr := h.ClientHost("box")
	ctx, cancel := context.WithCancel(context.Background())
	p, err := tr.Start(ctx, "bridge")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Wait = nil, want the error of a killed bridge")
		}
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("the bridge kept running after the context of Start ended")
	}
	if n := tr.Open(); n != 0 {
		t.Errorf("%d bridges open, want 0", n)
	}
}

// A missing folder comes back to the client as a question. The start that
// comes again with MakeFolder makes the folder.
func TestBridgeMissingFolder(t *testing.T) {
	h := hosttest.New(t)
	c := dial(t, h)
	if !c.HasFeature("mkdir") {
		t.Fatalf("features %q, want mkdir", c.Features)
	}
	ctx := context.Background()
	dir := filepath.Join(h.Home, "src", "new-app")
	a := client.NewArgs{Argv: []string{"true"}, Cwd: "~/src/new-app"}
	_, err := c.NewSession(ctx, a)
	q, ok := client.FolderQuestionOf(err)
	if !ok || q.Folder != dir {
		t.Fatalf("new in a missing folder = %v, want a question about %s", err, dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the question made the folder: %v", err)
	}
	a.MakeFolder = true
	s, err := c.NewSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || s.Cwd != dir {
		t.Fatalf("folder %v, session's folder %s", err, s.Cwd)
	}
	// A file in the way is not a folder, and is never made into one.
	if err := os.WriteFile(filepath.Join(h.Home, "a-file"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = c.NewSession(ctx, client.NewArgs{Argv: []string{"true"}, Cwd: "~/a-file", MakeFolder: true})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "bad_folder" {
		t.Fatalf("new in a file = %v", err)
	}
}
