package tui

import (
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher's memory (decision 0011): the sessions each host had when
// last seen, and the session last attached to. With it the launcher draws at
// once, before SSH has connected, and opens where you left off, so Enter
// takes you straight back in. It is the session cache of
// internal/client/seen.go, which the command line shares.

// memory is what the launcher reads of the session cache.
type memory = client.SessionCache

// lastSession is the session last attached to.
type lastSession = client.LastSession

func lastPath() string { return filepath.Join(client.StateDir(), "last.json") }

func saveMemory(mem memory) error {
	client.RememberSessions(mem.Sessions)
	return nil
}

// RememberSessions writes the lists that hosts answered with to the session
// cache, so that the picker and the launcher draw them at once next time.
// A host that is not in lists keeps what the cache had.
func RememberSessions(lists map[string][]client.Session) {
	client.RememberSessions(lists)
}

// LastSession is the session the launcher last attached to, for
// `attach -`.
func LastSession() (host, id string, ok bool) {
	last, ok := client.LoadLast()
	if !ok {
		return "", "", false
	}
	return last.Host, last.ID, true
}

// RememberLast records a session attached to, so the launcher and
// `attach -` find it next time.
func RememberLast(host, id, name string) {
	client.RememberLast(host, id, name)
}

// remember writes the memory in the background: the sessions of every host
// that answered, each with the time when its list arrived, not the time of
// the write. A host whose connection failed since keeps that older time,
// so a command asks it again. The launcher keeps, for itself, what the
// others had before. The cache keeps the lists of the hosts that it does
// not write.
func (m *Model) remember() func() {
	mem := memory{Sessions: map[string][]client.Session{}, Last: m.mem.Last}
	lists := map[string]client.HostList{}
	for _, h := range m.hosts {
		switch {
		case h.loaded:
			list := append([]client.Session(nil), h.sessions...)
			mem.Sessions[h.name()] = list
			lists[h.name()] = client.HostList{Sessions: list, At: h.seenAt}
		case len(m.mem.Sessions[h.name()]) > 0:
			mem.Sessions[h.name()] = m.mem.Sessions[h.name()]
		}
	}
	m.mem = mem
	be := m.be
	return func() { be.Remember(lists) }
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
