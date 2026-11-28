package guest

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// serveHolder relays one link.KindHolder stream byte-for-byte to the guest
// holder's Unix socket. It never inspects or modifies the stream's bytes.
func (r *runner) serveHolder(ctx context.Context, st *link.Stream) {
	defer st.Close()
	var hdr HolderHeader
	if err := st.Header(&hdr); err != nil || hdr.Session == "" {
		return
	}
	r.mu.Lock()
	mh := r.holders[hdr.Session]
	r.mu.Unlock()
	if mh == nil {
		return
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "unix", mh.sock)
	if err != nil {
		return
	}
	defer conn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		copyOneWay(conn, st)
	}()
	go func() {
		defer wg.Done()
		copyOneWay(st, conn)
	}()
	wg.Wait()
}

// servePut receives one pasted file on a link.KindPut stream, verifies its size
// and SHA-256 hash, stores it in the guest's private paste directory (mode 0600)
// and writes a single JSON PutResp before closing.
func (r *runner) servePut(st *link.Stream) {
	defer st.Close()
	reply := func(resp PutResp) {
		b, _ := json.Marshal(resp)
		_, _ = st.Write(b)
	}
	var hdr PutHeader
	if err := st.Header(&hdr); err != nil {
		reply(PutResp{Error: &proto.Error{Code: "bad_request", Message: "invalid put header"}})
		return
	}
	if hdr.Size < 0 || hdr.Size > host.MaxUpload {
		reply(PutResp{Error: &proto.Error{Code: "too_big", Message: "the file is larger than 16 MiB"}})
		return
	}
	dir := filepath.Join(r.runDir, "paste")
	if err := paths.EnsurePrivateDir(dir); err != nil {
		reply(PutResp{Error: &proto.Error{Code: "upload", Message: err.Error()}})
		return
	}
	cleanPasteDir(dir, 24*time.Hour)

	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		reply(PutResp{Error: &proto.Error{Code: "upload", Message: err.Error()}})
		return
	}
	tmpPath := f.Name()
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(st, host.MaxUpload+1))
	if err != nil {
		reply(PutResp{Error: &proto.Error{Code: "upload", Message: "the upload was cut short", Hint: "Try again."}})
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if n != hdr.Size || (hdr.SHA256 != "" && !strings.EqualFold(sum, hdr.SHA256)) {
		reply(PutResp{Error: &proto.Error{Code: "upload", Message: "the upload was damaged in transit", Hint: "Try again."}})
		return
	}
	if err := f.Close(); err != nil {
		reply(PutResp{Error: &proto.Error{Code: "upload", Message: err.Error()}})
		return
	}
	name := host.SafeName(hdr.Name)
	final := filepath.Join(dir, sum[:12]+"-"+name)
	if err := os.Rename(tmpPath, final); err != nil {
		reply(PutResp{Error: &proto.Error{Code: "upload", Message: err.Error()}})
		return
	}
	_ = os.Chmod(final, 0o600)
	keep = true
	reply(PutResp{Path: final, SHA256: sum})
}

func cleanPasteDir(dir string, age time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > age {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (r *runner) readHolderEvents(sessionID string, evR *os.File, started chan<- host.GuestEvent) {
	defer evR.Close()
	sentStart := false
	defer func() {
		if !sentStart {
			close(started)
		}
	}()
	sc := bufio.NewScanner(evR)
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		var ge host.GuestEvent
		if json.Unmarshal(sc.Bytes(), &ge) != nil {
			continue
		}
		if ge.Session == "" {
			ge.Session = sessionID
		}
		if !sentStart && (ge.Type == "started" || ge.Type == "start_failed") {
			sentStart = true
			started <- ge
			close(started)
			continue
		}
		r.sendEvent(Event{
			Type:       ge.Type,
			Session:    ge.Session,
			State:      ge.State,
			Source:     ge.Source,
			Reason:     ge.Reason,
			FullScreen: ge.FullScreen,
			Kind:       ge.Kind,
			Detail:     ge.Detail,
			TurnNS:     ge.TurnNS,
			AgentID:    ge.AgentID,
			IDSource:   ge.IDSource,
			ExitCode:   ge.ExitCode,
			Signal:     ge.Signal,
			Cols:       ge.Cols,
			Rows:       ge.Rows,
		})
	}
}
