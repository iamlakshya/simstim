package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Removal is the record of a `host rm` that has not finished
// (docs/contributing/cli-spec.md 11.3, rule 10). Each part keeps its
// name in it when it ends, such as "destroy". The record says what a run
// after a failure has to finish. It never decides that a machine is gone:
// only its provider says that.
type Removal struct {
	// Host is the host's name, in its exact case.
	Host string `json:"host"`
	// Provider, Machine and Created name the machine, when the host has
	// one: its provider, its ID, and when `host create` made it. A record
	// of another machine with the same host name does not count.
	Provider string `json:"provider,omitempty"`
	Machine  string `json:"machine,omitempty"`
	Created  string `json:"created,omitempty"`
	// Done lists the parts that ended, in the order they ended.
	Done []string `json:"done"`
}

// The parts of a removal.
const (
	RemovalDestroy = "destroy"
)

// removalPath is the file of the record of host name. Its folder holds the
// name in lower case and a hash of the name in its exact case, so Box and
// box keep two records, also on a file system that ignores case. A name
// that cannot name a host, such as one that a hand edit of hosts.toml gave
// a "/", gets no record: the path is "".
func removalPath(name string) string {
	dir := removalDir(name)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "removal.json")
}

// removalDir is the folder of the record of host name, or "".
func removalDir(name string) string {
	if !ValidHostName(name) {
		return ""
	}
	return filepath.Join(StateDir(), "hosts", HostFileKey(name))
}

// newRemoval is an empty record of h's removal.
func newRemoval(h Host) Removal {
	r := Removal{Host: h.Name}
	if m := h.Machine; m != nil {
		r.Provider, r.Machine, r.Created = m.Provider, m.ID, m.Created
	}
	return r
}

// LoadRemoval returns the record of h's removal: the parts that a run of
// `host rm` before this one ended. It is empty when there is none, or when
// the record is of another host or another machine.
func LoadRemoval(h Host) Removal {
	r := newRemoval(h)
	path := removalPath(h.Name)
	if path == "" {
		return r
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	var saved Removal
	if json.Unmarshal(b, &saved) != nil || saved.Host != r.Host || saved.Provider != r.Provider || saved.Machine != r.Machine || saved.Created != r.Created {
		return r
	}
	return saved
}

// Has reports whether part ended.
func (r Removal) Has(part string) bool { return slices.Contains(r.Done, part) }

// Record keeps part as ended, and saves the record.
func (r *Removal) Record(part string) error {
	if !r.Has(part) {
		r.Done = append(r.Done, part)
	}
	path := removalPath(r.Host)
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("record the removal of %s: %w", r.Host, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record the removal of %s: %w", r.Host, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("record the removal of %s: %w", r.Host, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("record the removal of %s: %w", r.Host, err)
	}
	return nil
}

// ClearRemoval deletes the record of host name, and its folder when that
// is then empty. It runs when the host is forgotten, the last part, and
// when `host create` makes a host of that name.
func ClearRemoval(name string) error {
	path := removalPath(name)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &UserError{Msg: fmt.Sprintf("could not clear the record of an earlier host rm of %s: %v", name, err),
			Hint: fmt.Sprintf("Delete %s by hand, then run the command again.", path), Code: "removal_record"}
	}
	_ = os.Remove(filepath.Dir(path)) // only when empty
	return nil
}
