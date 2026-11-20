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

// memoryPath is a private file in the client's state folder.
func memoryPath() string { return filepath.Join(client.StateDir(), "launcher.json") }

func loadMemory() memory {
	var mem memory
	if b, err := os.ReadFile(memoryPath()); err == nil {
		_ = json.Unmarshal(b, &mem)
	}
	return mem
}

func saveMemory(mem memory) error {
	p := memoryPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(p), err)
	}
	b, err := json.Marshal(mem)
	if err != nil {
		return fmt.Errorf("save the launcher's memory: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("save the launcher's memory: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("save the launcher's memory: %w", err)
	}
	return nil
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

// RememberLast records a session attached from the command line, so the
// launcher and `attach -` find it next time.
func RememberLast(host, id, name string) {
	mem := loadMemory()
	mem.Last = &lastSession{Host: host, ID: id, Name: name, At: time.Now()}
	_ = saveMemory(mem)
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
