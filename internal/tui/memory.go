package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher's memory (decision 0011): the sessions each host had when
// last seen, and the session last attached to. With it the launcher draws at
// once, before SSH has connected, and opens where you left off, so Enter
// takes you straight back in.
//
// It is a cache, and advisory: two writers at once can lose one change,
// and the next list mends a lost list. The session last attached to is in
// a file of its own, so the writers of lists never touch it.

type memory struct {
	Sessions map[string][]client.Session `json:"sessions,omitempty"`
	Last     *lastSession                `json:"last,omitempty"`
}

type lastSession struct {
	Host string    `json:"host"`
	ID   string    `json:"id"`
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

// memoryPath is a private file in the client's state folder, with the
// sessions of each host. lastPath, beside it, has the session last attached
// to.
func memoryPath() string { return filepath.Join(client.StateDir(), "launcher.json") }
func lastPath() string   { return filepath.Join(client.StateDir(), "last.json") }

// loadMemory reads both files. A launcher.json from before last.json may
// hold the last session itself: it counts until last.json is written.
func loadMemory() memory {
	var mem memory
	if b, err := os.ReadFile(memoryPath()); err == nil {
		_ = json.Unmarshal(b, &mem)
	}
	if b, err := os.ReadFile(lastPath()); err == nil {
		var last lastSession
		if json.Unmarshal(b, &last) == nil {
			mem.Last = &last
		}
	}
	return mem
}

// saveMemory writes the sessions of mem. It never writes the last session,
// except to keep the one of an older launcher.json when last.json does not
// exist yet: then it makes last.json, and never replaces one.
func saveMemory(mem memory) error {
	if mem.Last != nil {
		if b, err := json.Marshal(mem.Last); err == nil {
			_ = makeOnce(lastPath(), b)
		}
	}
	return writeFile(memoryPath(), memory{Sessions: mem.Sessions})
}

// saveLast writes the session last attached to.
func saveLast(last lastSession) error { return writeFile(lastPath(), last) }

// makeOnce makes the file p with b in it, unless p exists.
func makeOnce(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(p), err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("make %s: %w", p, err)
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("write %s: %w", p, werr)
	}
	return nil
}

// writeFile writes v to p as JSON, through a temporary file and a rename.
func writeFile(p string, v any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(p), err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("save the launcher's memory: %w", err)
	}
	// Each writer has its own temporary file, so that two writers, such as
	// the launcher and a command, never rename a half-written one.
	f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*.tmp")
	if err != nil {
		return fmt.Errorf("save the launcher's memory: %w", err)
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, p)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save the launcher's memory: %w", werr)
	}
	return nil
}

// RememberSessions writes the lists that hosts answered with to the session
// cache, so that the picker and the launcher draw them at once next time.
// A host that is not in lists keeps what the cache had.
func RememberSessions(lists map[string][]client.Session) {
	if len(lists) == 0 {
		return
	}
	mem := loadMemory()
	if mem.Sessions == nil {
		mem.Sessions = map[string][]client.Session{}
	}
	for h, l := range lists {
		mem.Sessions[h] = append([]client.Session(nil), l...)
	}
	_ = saveMemory(mem)
}

// LastSession is the session the launcher last attached to, for
// `attach -`.
func LastSession() (host, id string, ok bool) {
	mem := loadMemory()
	if mem.Last == nil || mem.Last.Host == "" || mem.Last.ID == "" {
		return "", "", false
	}
	return mem.Last.Host, mem.Last.ID, true
}

// RememberLast records a session attached to, so the launcher and
// `attach -` find it next time.
func RememberLast(host, id, name string) {
	_ = saveLast(lastSession{Host: host, ID: id, Name: name, At: time.Now()})
}

// remember writes the memory in the background: the sessions of every host
// that answered, and what the others had before.
func (m *Model) remember() func() {
	mem := memory{Sessions: map[string][]client.Session{}, Last: m.mem.Last}
	for _, h := range m.hosts {
		switch {
		case h.loaded:
			mem.Sessions[h.name()] = append([]client.Session(nil), h.sessions...)
		case len(m.mem.Sessions[h.name()]) > 0:
			mem.Sessions[h.name()] = m.mem.Sessions[h.name()]
		}
	}
	m.mem = mem
	be := m.be
	return func() { be.Remember(mem) }
}

// restore shows what the memory knows before any host has answered, and
// picks the session last attached to.
func (m *Model) restore(switcher bool) {
	m.mem = m.be.Memory()
	for _, h := range m.hosts {
		if list := m.mem.Sessions[h.name()]; len(list) > 0 {
			// An older version kept rows as the host sent them.
			h.sessions = client.ValidSessions(h.name(), list)
		}
	}
	last := m.mem.Last
	if last != nil && !api.ValidSessionID(last.ID) {
		m.mem.Last, last = nil, nil // no request could name it
	}
	if last == nil || switcher || m.hostByName(last.Host) == nil {
		return
	}
	m.screen, m.cur = scrSessions, last.Host
	m.want = sessKey{last.Host, last.ID}
	m.autoSel = false
}
