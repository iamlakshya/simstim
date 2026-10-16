package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The bridge's part of the bundle (decision 0025). debug.collect gathers
// the host's files for a bundle: the bridges' debug files, and for each
// session it covers, its records, holder.log, its profiles and saved
// windows, fresh heap and goroutine profiles, the unit's state and the
// kernel's report. debug.read then sends each one, 1 MiB at a time, by its
// name in the bundle, never by a path. It replaces the values of the
// alerts file in each before it goes, so they never leave the host.

// collected is one file that debug.read may send: a file on the host, or
// text that debug.collect made, such as the unit's state.
type collected struct {
	path string
	data []byte
	made bool // data holds the file, which debug.collect made
	text bool // the host replaces the alerts file's values in it
}

// bundleFiles is what the last debug.collect of a connection listed.
type bundleFiles struct {
	mu      sync.Mutex
	files   map[string]collected
	secrets []alertValue
}

// handleBundle serves debug.collect and debug.read.
func (b *bridge) handleBundle(req proto.Request) {
	if req.Op == api.OpDebugCollect {
		var a api.DebugCollectArgs
		if len(req.Args) > 0 && json.Unmarshal(req.Args, &a) != nil {
			b.reply(req.ID, nil, badDebugArgs())
			return
		}
		b.reply(req.ID, b.collect(a), nil)
		return
	}
	var a api.DebugReadArgs
	if json.Unmarshal(req.Args, &a) != nil {
		b.reply(req.ID, nil, badDebugArgs())
		return
	}
	v, err := b.readCollected(a)
	b.reply(req.ID, v, err)
}

// collect lists the host's part of a bundle, and keeps the list for
// debug.read.
func (b *bridge) collect(a api.DebugCollectArgs) api.DebugCollected {
	home, _ := os.UserHomeDir()
	files := map[string]collected{}
	addFolder(files, paths.DebugDir(), "host/bridge/")
	ids, notes := bundleSessions(a)
	for _, id := range ids {
		notes = append(notes, collectSession(id, files)...)
	}
	secrets := alertSecrets()
	out := api.DebugCollected{Home: home, Notes: notes}
	for name, f := range files {
		if !f.text && len(secrets) > 0 && holdsSecret(f.path, secrets) {
			delete(files, name)
			out.Notes = append(out.Notes, name+": left out, because it holds a value of the alerts file")
			continue
		}
		size := int64(len(f.data))
		if !f.made {
			size = fileSize(f.path)
		}
		out.Files = append(out.Files, api.DebugFile{Path: name, Size: size})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	b.bun.mu.Lock()
	b.bun.files, b.bun.secrets = files, secrets
	b.bun.mu.Unlock()
	b.log.Debug("bundle collected", "files", len(out.Files), "sessions", len(ids))
	return out
}

// bundleSessions are the sessions that a bundle covers: those that a names,
// or with All, every session with debug files. The notes say which named
// session does not exist.
func bundleSessions(a api.DebugCollectArgs) (ids, notes []string) {
	if !a.All {
		for _, id := range a.IDs {
			if !validID(id) || !isDir(paths.SessionDir(id)) {
				notes = append(notes, "no session "+id+" on this host")
				continue
			}
			ids = append(ids, id)
		}
		return ids, notes
	}
	entries, _ := os.ReadDir(paths.SessionsDir())
	for _, e := range entries {
		id := e.Name()
		if e.IsDir() && validID(id) && hasDebugFiles(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// hasDebugFiles reports whether a session has records, profiles or saved
// windows.
func hasDebugFiles(id string) bool {
	for _, p := range []string{paths.DebugTrace(id), paths.DebugTrace(id) + ".1"} {
		if fileSize(p) > 0 {
			return true
		}
	}
	entries, _ := os.ReadDir(paths.SessionDebugDir(id))
	return len(entries) > 0
}

// collectSession adds one session's files, after fresh heap and goroutine
// profiles of its holder. It returns notes on what it could not gather.
func collectSession(id string, files map[string]collected) []string {
	var notes []string
	prefix := "host/sessions/" + id + "/"
	if alive(id) {
		for _, kind := range []string{diag.ProfileHeap, diag.ProfileGoroutine} {
			if _, err := HolderRequest(id, api.OpDebugProfile, api.DebugProfileArgs{ID: id, Kind: kind}, holderWait); err != nil {
				notes = append(notes, fmt.Sprintf("%s: no fresh %s profile: %v", id, kind, err))
			}
		}
	} else {
		notes = append(notes, id+": its holder is not running, so there are no fresh profiles")
	}
	for _, p := range []string{paths.DebugTrace(id), paths.HolderLog(id)} {
		addFile(files, p, prefix+filepath.Base(p))
		addFile(files, p+".1", prefix+filepath.Base(p)+".1")
	}
	addFolder(files, paths.SessionDebugDir(id), prefix+"debug/")
	m, err := LoadManifest(id)
	if err != nil || m.Run.Unit == "" {
		return append(notes, id+": no systemd unit, so no unit state and no kernel report")
	}
	if st, err := unitState(m.Run.Unit); err == nil {
		files[prefix+"unit.txt"] = collected{data: st, made: true, text: true}
	} else {
		notes = append(notes, fmt.Sprintf("%s: no unit state: %v", id, err))
	}
	if !JournalAccess() {
		return append(notes, id+": no kernel report, because this user cannot read the system journal. unit.txt has the unit's result and memory.events")
	}
	if rep, err := kernelReport(m.Run.Unit, m.CreatedAt); err == nil {
		files[prefix+"kernel.txt"] = collected{data: rep, made: true, text: true}
	} else {
		notes = append(notes, fmt.Sprintf("%s: no kernel report: %v", id, err))
	}
	return notes
}

// addFolder adds each file in dir whose name the debug mode gives its
// files, as prefix and its name. Any other file stays on the host.
func addFolder(files map[string]collected, dir, prefix string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if diag.IsDebugName(e.Name()) {
			addFile(files, filepath.Join(dir, e.Name()), prefix+e.Name())
		}
	}
}

// addFile adds the file at path, when it is a regular file, as name. The
// read checks the file again, on the file that it opens.
func addFile(files map[string]collected, path, name string) {
	if st, err := os.Lstat(path); err == nil && st.Mode().IsRegular() && st.Size() <= collectMax {
		files[name] = collected{path: path, text: textFile(name)}
	}
}

// textFile reports whether a file of the bundle is text: anything but a
// profile or a saved window.
func textFile(name string) bool {
	return !strings.HasSuffix(name, ".pprof") && !strings.HasSuffix(name, ".trace")
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// readCollected sends up to api.DebugReadMax bytes of a file that the last
// debug.collect listed, from an offset, with the alerts file's values
// replaced and the bodies of HTTP errors hidden. A part of a text file ends
// at a line's end, so that no value is cut in two. The values hold no line
// break.
func (b *bridge) readCollected(a api.DebugReadArgs) (api.DebugChunk, error) {
	b.bun.mu.Lock()
	f, ok := b.bun.files[a.Name]
	secrets := b.bun.secrets
	b.bun.mu.Unlock()
	if !ok {
		return api.DebugChunk{}, UserError("not_found", "the host did not list "+a.Name+" for this bundle", "Run debug bundle again.")
	}
	if a.Offset < 0 {
		return api.DebugChunk{}, UserError("bad_request", "a negative offset", "Update the client.")
	}
	buf, eof, err := readPart(f, a.Offset)
	if err != nil {
		return api.DebugChunk{}, UserError("read", "cannot read "+a.Name+": "+err.Error(), "Run debug bundle again.")
	}
	if f.text && !eof {
		i := bytes.LastIndexByte(buf, '\n')
		if i < 0 {
			return api.DebugChunk{}, UserError("line_too_long", a.Name+" has a line of more than 1 MiB, so the host cannot check it for the alerts file's values",
				"The bundle leaves the file out. Copy it by hand, and read it first.")
		}
		buf = buf[:i+1]
	}
	next := a.Offset + int64(len(buf))
	var n int
	if f.text {
		buf, n = replaceSecrets(buf, secrets)
	}
	return api.DebugChunk{Data: buf, Next: next, EOF: eof, Hidden: n}, nil
}

// readPart reads up to api.DebugReadMax bytes of f from off, and reports
// whether they end the file.
func readPart(f collected, off int64) ([]byte, bool, error) {
	if f.made {
		start := min(off, int64(len(f.data)))
		end := min(start+api.DebugReadMax, int64(len(f.data)))
		return bytes.Clone(f.data[start:end]), end == int64(len(f.data)), nil
	}
	file, err := diag.OpenPlain(f.path)
	if err != nil {
		return nil, false, fmt.Errorf("it is gone, or no longer a plain file: %w", err)
	}
	defer file.Close()
	buf := make([]byte, api.DebugReadMax)
	n, err := file.ReadAt(buf, off)
	switch {
	case errors.Is(err, io.EOF):
		return buf[:n], true, nil
	case err != nil:
		return nil, false, fmt.Errorf("read: %w", err)
	}
	return buf[:n], false, nil
}
