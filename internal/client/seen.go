package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The session cache (decision 0011, docs/contributing/cli-spec.md 9.2): the
// sessions each host had when last seen, when each host's list arrived,
// and the session last attached to. The launcher draws from it at once,
// before SSH has connected, and opens where you left off. The picker
// starts from it, and an address without a host learns from it which host
// to ask. What a command acts on is always decided by the host's own list,
// never by the cache.
//
// It is a cache, and advisory. Each writer reads the file first, changes
// only its own hosts, and saves through a temporary file of its own and a
// rename, so no writer renames a half-written file. Two writers at once
// can still lose one change, and the next list mends a lost list. The
// session last attached to is in a file of its own: only an attach writes
// it, and the writers of lists never touch it.

// SessionCache is what the session cache holds.
type SessionCache struct {
	// Sessions holds each host's sessions when last seen.
	Sessions map[string][]Session `json:"sessions,omitempty"`
	// Seen holds when each host's list arrived from the host.
	Seen map[string]time.Time `json:"seen,omitempty"`
	// Last is the session last attached to. It lives in last.json. A
	// launcher.json from before last.json may hold it too.
	Last *LastSession `json:"last,omitempty"`
}

// LastSession is the session last attached to.
type LastSession struct {
	Host string    `json:"host"`
	ID   string    `json:"id"`
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

// HostList is one host's list of sessions, and when it arrived from the
// host.
type HostList struct {
	Sessions []Session
	At       time.Time
}

// cacheNow is the clock of the cache. Tests set it.
var cacheNow = time.Now

// staleTemp is the age after which a temporary file that a writer left,
// as at a crash, is removed.
const staleTemp = time.Hour

// cachePath is a private file in the client's state folder, with the
// sessions of each host. lastPath, beside it, has the session last
// attached to.
func cachePath() string { return filepath.Join(StateDir(), "launcher.json") }
func lastPath() string  { return filepath.Join(StateDir(), "last.json") }

// LoadSessionCache reads the session cache. A launcher.json from before
// last.json may hold the last session itself: it counts until last.json
// is written. A file that is missing or does not parse reads as empty.
func LoadSessionCache() SessionCache {
	var c SessionCache
	if b, err := os.ReadFile(cachePath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if b, err := os.ReadFile(lastPath()); err == nil {
		var last LastSession
		if json.Unmarshal(b, &last) == nil {
			c.Last = &last
		}
	}
	return c
}

// RememberSessions writes the lists that hosts answered with just now to
// the session cache. A host that is not in lists keeps what the cache had.
func RememberSessions(lists map[string][]Session) {
	now := cacheNow().UTC()
	at := make(map[string]HostList, len(lists))
	for h, l := range lists {
		at[h] = HostList{Sessions: l, At: now}
	}
	RememberLists(at)
}

// RememberLists writes each host's list with the time when it arrived from
// the host, which the cache keeps as it is. A list older than the one that
// the cache has for its host changes nothing, and so does one without a
// time, unless the cache's time is in the future, as after the clock was
// set back. A host that is not in lists keeps what the cache had.
func RememberLists(lists map[string]HostList) {
	c := LoadSessionCache()
	changed := false
	for h, l := range lists {
		old := c.Seen[h]
		if l.At.IsZero() || l.At.Before(old) && !old.After(cacheNow()) {
			continue
		}
		if c.Sessions == nil {
			c.Sessions = map[string][]Session{}
		}
		if c.Seen == nil {
			c.Seen = map[string]time.Time{}
		}
		c.Sessions[h] = append([]Session(nil), l.Sessions...)
		c.Seen[h] = l.At.UTC()
		changed = true
	}
	if changed {
		_ = saveSessionCache(c)
	}
}

// RememberSession puts the session s, which a command just started or
// renamed, in host's cached list, in place of the row with its ID. The
// time of the list stays: the rest of the list did not arrive.
func RememberSession(host string, s Session) {
	editList(host, func(list []Session) []Session {
		for i := range list {
			if list[i].ID == s.ID {
				list[i] = s
				return list
			}
		}
		return append(list, s)
	})
}

// ForgetSession takes the session id, which a command just deleted, out of
// host's cached list. The time of the list stays.
func ForgetSession(host, id string) {
	editList(host, func(list []Session) []Session {
		out := list[:0]
		for _, s := range list {
			if s.ID != id {
				out = append(out, s)
			}
		}
		return out
	})
}

// editList changes host's cached list with edit, and saves it.
func editList(host string, edit func([]Session) []Session) {
	c := LoadSessionCache()
	if c.Sessions == nil {
		c.Sessions = map[string][]Session{}
	}
	c.Sessions[host] = edit(append([]Session(nil), c.Sessions[host]...))
	_ = saveSessionCache(c)
}

// saveSessionCache writes the lists of c. It never writes the last
// session, except to keep the one of an older launcher.json when last.json
// does not exist yet: then it makes last.json, and never replaces one.
func saveSessionCache(c SessionCache) error {
	if c.Last != nil {
		if b, err := json.Marshal(c.Last); err == nil {
			_ = makeOnce(lastPath(), b)
		}
	}
	removeStaleTemps()
	return writeCacheFile(cachePath(), SessionCache{Sessions: c.Sessions, Seen: c.Seen})
}

// LoadLast is the session last attached to, for the address "-" and for
// the launcher's start.
func LoadLast() (LastSession, bool) {
	c := LoadSessionCache()
	if c.Last == nil || c.Last.Host == "" || c.Last.ID == "" {
		return LastSession{}, false
	}
	return *c.Last, true
}

// RememberLast records a session attached to, so that the launcher and
// the address "-" find it next time.
func RememberLast(host, id, name string) {
	_ = writeCacheFile(lastPath(), LastSession{Host: host, ID: id, Name: name, At: time.Now()})
}

// makeOnce makes the file p with b in it, unless p exists. It writes a
// temporary file first, and links it to p, which fails when p exists, so p
// is never half written.
func makeOnce(p string, b []byte) error {
	tmp, err := writeTemp(p, b)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, p); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("make %s: %w", p, err)
	}
	return nil
}

// writeCacheFile writes v to p as JSON, through a temporary file and a
// rename.
func writeCacheFile(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("save the session cache: %w", err)
	}
	tmp, err := writeTemp(p, b)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save the session cache: %w", err)
	}
	return nil
}

// writeTemp writes b to a new private temporary file beside p, and returns
// its path. Each writer has its own temporary file, so that two writers,
// such as the launcher and a command, never rename a half-written one.
func writeTemp(p string, b []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", fmt.Errorf("make %s: %w", filepath.Dir(p), err)
	}
	f, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("save the session cache: %w", err)
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("save the session cache: %w", werr)
	}
	return f.Name(), nil
}

// removeStaleTemps removes the temporary files of the cache that a writer
// left, as at a crash, once they are older than staleTemp. A writer that
// runs now has a newer one.
func removeStaleTemps() {
	for _, p := range []string{cachePath(), lastPath()} {
		dir, prefix := filepath.Dir(p), filepath.Base(p)+"."
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".tmp") {
				continue
			}
			f := filepath.Join(dir, name)
			if fi, err := os.Stat(f); err == nil && cacheNow().Sub(fi.ModTime()) > staleTemp {
				_ = os.Remove(f)
			}
		}
	}
}
