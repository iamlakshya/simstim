package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The pin's file: ~/.config/<name>/ssh/known_hosts-<key>, 0600, where the
// key is HostFileKey of the host's name. Its first line names the exact
// host that it is the pin of, and it holds the host's keys under the name
// <name>-<host> (spec 14.5 and 14.7.1). Two hosts never share a pin, and a
// pin whose first line names another host is never used, nor replaced. A
// command that adds a host keeps its new pin apart, in a staged file, and
// moves it to its place only when it saves the host (pin_stage.go).

// pinAlias is the name under which the pin holds the host's key, and which
// ssh looks up there (HostKeyAlias).
func pinAlias(name string) string { return brand.Name + "-" + name }

// pinHeader is the first line of the pin of the host called name.
func pinHeader(name string) string { return "# " + brand.Name + " pin of the host " + name }

// pinOwner is the host that the first line of a pin names. ok is false for
// a file whose first line is not a pin's.
func pinOwner(b []byte) (owner string, ok bool) {
	first, _, _ := strings.Cut(string(b), "\n")
	if owner, ok = strings.CutPrefix(strings.TrimRight(first, "\r"), pinHeader("")); !ok {
		return "", false
	}
	return owner, true
}

// pinPath is the pin of the host called name. It is "" for a name that
// cannot name a host, such as one with a slash, which never gets a pin.
func pinPath(name string) string {
	if !ValidHostName(name) {
		return ""
	}
	return filepath.Join(ConfigDir(), "ssh", "known_hosts-"+HostFileKey(name))
}

// legacyPinPath is where the first version of pins kept the pin of the host
// called name, named after the host alone. migratePin moves it.
func legacyPinPath(name string) string {
	if !ValidHostName(name) {
		return ""
	}
	return filepath.Join(ConfigDir(), "ssh", "known_hosts-"+name)
}

// lockFile is the lock that pins of the host called name take. It is never
// deleted, so every client that takes it locks one file, also while a host
// is forgotten and added again.
func lockFile(name string) string {
	if !ValidHostName(name) {
		return ""
	}
	return filepath.Join(ConfigDir(), "ssh", HostFileKey(name)+".lock")
}

// foreignPinError refuses the file at the pin's path of the host called
// name when its first line does not name that exact host: owner is the host
// that it names, or "" for none. The client neither uses nor replaces it.
func foreignPinError(name, path, owner string) *UserError {
	what := "names no host"
	if owner != "" {
		what = fmt.Sprintf("is the pin of %q", CleanLine(owner))
	}
	return &UserError{
		Msg:  fmt.Sprintf("the file %s, where the pinned host key of %s belongs, %s, so the client does not use it", path, name, what),
		Hint: fmt.Sprintf("Check that file. Remove it only if no host uses it, then: %s host rekey %s", brand.Name, name),
		Code: "host_key_unreadable", Kind: KindRefused,
	}
}

// ownPin checks that b, the file at the pin's path of the host called name,
// is that host's pin.
func ownPin(name, path string, b []byte) error {
	if owner, _ := pinOwner(b); owner != name {
		return foreignPinError(name, path, owner)
	}
	return nil
}

// isForeign reports whether err refuses a pin of another host.
func isForeign(err error) bool {
	var ue *UserError
	return errors.As(err, &ue) && ue.Code == "host_key_unreadable" && strings.HasPrefix(ue.Msg, "the file ")
}

// migratePin moves the pin of the host called name from where the first
// version of pins kept it, under the pin's lock: once, for a saved host,
// and only when every key line in it names this exact host, which tells it
// from the pin of a name that differs only in case on a disk that ignores
// case. It removes the old file only when the pin in its place holds the
// same keys. A pin that it cannot move stays where it is, and the host gets
// a new pin, as a host saved before pins does.
//
// It takes the pin's lock, so it never runs while this process holds it.
func migratePin(name string) { _ = migratePinContext(context.Background(), name) }

// migratePinContext is the context-aware migration. Non-context failures
// retain the legacy file for a later retry. Cancellation is returned to the
// caller, which must not continue by treating it as an absent pin.
func migratePinContext(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	old := legacyPinPath(name)
	if old == "" || HasPin(name) {
		return nil
	}
	if _, err := os.Lstat(old); err != nil {
		return nil
	}
	unlock, err := lockPin(ctx, name)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	defer unlock()
	b, err := os.ReadFile(old)
	if err != nil {
		return nil
	}
	keys := readKeys(b)
	if len(keys) == 0 || !namesOnly(b, pinAlias(name)) {
		return nil
	}
	if ok, err := savedHost(name); err != nil || !ok {
		return nil
	}
	beforePinMigrationWrite(name)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !HasPin(name) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeKeys(pinPath(name), name, keys); err != nil {
			return nil
		}
		pinMigrationWritten(name)
		DebugLog().Info("moved the pinned host key to its new file", "host", name)
	}
	if now, err := readPin(name); err == nil && sameKeys(now, keys) {
		_ = os.Remove(old)
	}
	return ctx.Err()
}

// sameKeys reports whether a and b hold the same keys.
func sameKeys(a, b []hostKey) bool {
	for _, k := range a {
		if !holds(b, k) {
			return false
		}
	}
	for _, k := range b {
		if !holds(a, k) {
			return false
		}
	}
	return true
}

// namesOnly reports whether every key line of a known hosts file names the
// host alias, as its exact host field.
func namesOnly(b []byte, alias string) bool {
	for _, l := range strings.Split(string(b), "\n") {
		if _, _, ok := parseKeyLine(l); ok && strings.Fields(l)[0] != alias {
			return false
		}
	}
	return true
}

// forgetPin removes the pin of the host called name, and a pin of the first
// version that names it. The caller holds the pin's lock, which stays. A
// file at the pin's path that is the pin of another host stays too.
func forgetPin(name string) error {
	if !ValidHostName(name) {
		return nil
	}
	p := pinPath(name)
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("read the pinned host key of %s: %w", name, err)
	case ownPin(name, p, b) != nil:
		DebugLog().Warn("left a pin that is not the host's", "host", name, "path", p)
		return ownPin(name, p, b)
	default:
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove the pinned host key of %s: %w", name, err)
		}
	}
	old := legacyPinPath(name)
	if b, err := os.ReadFile(old); err == nil && namesOnly(b, pinAlias(name)) && len(readKeys(b)) > 0 {
		_ = os.Remove(old)
	}
	return nil
}

// savedHost reports whether hosts.toml holds a host called name. A file
// that cannot be read is an error, never a no: a no would let a saved host
// go without a pin.
func savedHost(name string) (bool, error) {
	hosts, err := LoadHosts()
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == name }), nil
}

// HasPin reports whether the host called name has a pin. Anything at the
// pin's path counts, even a file that cannot be read: the client then
// refuses the connection, which is safer than a connection without the pin.
func HasPin(name string) bool {
	p := pinPath(name)
	if p == "" {
		return false
	}
	_, err := os.Lstat(p)
	return err == nil
}

// pinned reports whether ssh commands of this process for the host called
// name check a pin: its own, or one staged for it.
func pinned(name string) bool {
	if _, ok := stagedPin(name); ok {
		return true
	}
	return HasPin(name)
}

// pinSnap is the pin that one ssh command for a host checks, read once:
// the file that ssh reads, and the keys in it, which name the socket of the
// shared connection. A command builds its options from one snapshot, so the
// check that it has a pin and the options that check the pin never differ.
type pinSnap struct {
	path   string
	keys   []hostKey
	staged bool
}

// loadPin is the pin that this process's ssh commands for name check: a
// staged pin, or the host's own. ok is false when there is none. A file at
// the pin's path that is not the host's pin, or holds no key, is an error:
// the command fails, and never goes without the pin.
func loadPin(name string) (snap pinSnap, ok bool, err error) {
	if st, ok := stagedPin(name); ok {
		return pinSnap{path: st.path, keys: st.keys, staged: true}, true, nil
	}
	p := pinPath(name)
	if p == "" {
		return pinSnap{}, false, nil
	}
	wantKey, kerr := recordedTierKey(name)
	if kerr != nil {
		return pinSnap{}, false, kerr
	}
	keys, err := readPin(name)
	if errors.Is(err, os.ErrNotExist) {
		if wantKey != "" {
			return pinSnap{}, false, missingPinError(name, wantKey)
		}
		return pinSnap{}, false, nil
	}
	if err != nil {
		if isForeign(err) {
			return pinSnap{}, false, err
		}
		return pinSnap{}, false, &UserError{Msg: fmt.Sprintf("%s's pinned host key cannot be read, so the client does not connect: %v", name, err),
			Hint: brand.Name + " host rekey " + name, Code: "host_key_unreadable", Kind: KindRefused}
	}
	if wantKey != "" {
		if _, found := withFingerprint(keys, wantKey); !found {
			return pinSnap{}, false, mismatchedTierKeyError(name, wantKey, pinOf(keys, "").Fingerprint)
		}
	}
	return pinSnap{path: p, keys: keys}, true, nil
}

// PinOf returns the pin of the host called name, with PinKept as its
// source.
func PinOf(name string) (Pin, error) {
	keys, err := readPin(name)
	if err != nil {
		return Pin{}, err
	}
	return pinOf(keys, PinKept), nil
}

// readPin reads the keys of a host's pin, once each. A file whose first
// line does not name this exact host is refused (foreignPinError).
func readPin(name string) ([]hostKey, error) {
	p := pinPath(name)
	if p == "" {
		return nil, fmt.Errorf("%q cannot name a host, so it has no pin", name)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read the pinned host key of %s: %w", name, err)
	}
	if err := ownPin(name, p, b); err != nil {
		return nil, err
	}
	keys := readKeys(b)
	if len(keys) == 0 {
		return nil, fmt.Errorf("the pin %s holds no key", p)
	}
	return keys, nil
}

// pinContent is the pin of the host called name that holds keys.
func pinContent(name string, keys []hostKey) []byte {
	var b strings.Builder
	b.WriteString(pinHeader(name) + "\n")
	fmt.Fprintf(&b, "# `%s host rekey %s` replaces it.\n", brand.Name, name)
	for _, k := range keys {
		b.WriteString(k.line(pinAlias(name)) + "\n")
	}
	return []byte(b.String())
}

// writeKeys writes the pin of the host called name, which holds keys, to
// path (replaceFile).
func writeKeys(path, name string, keys []hostKey) error {
	if path == "" || len(keys) == 0 {
		return fmt.Errorf("no pin to write for %q", name)
	}
	if err := replaceFile(path, name, pinContent(name, keys)); err != nil {
		return fmt.Errorf("write the pinned host key of %s: %w", name, err)
	}
	return nil
}

// replaceFile writes data to path through a temporary file in the same
// folder, so a reader sees the old file or the new one, never a part. The
// file is 0600.
func replaceFile(path, name string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	// CreateTemp makes the file with mode 0600, before anything is in it.
	f, err := os.CreateTemp(dir, ".known_hosts-"+HostFileKey(name)+"-*")
	if err != nil {
		return fmt.Errorf("make a file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // gone after the rename; left only by a failure
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writePin replaces the pin of the host called name with keys. While this
// process has a stage open for name, the keys go to the stage instead. It
// never replaces a file at the pin's path that is the pin of another host.
// The caller holds the pin's lock.
func writePin(name string, st *stage, keys []hostKey) error {
	if err := sameStage(name, st); err != nil {
		return err
	}
	if st != nil {
		if err := writeKeys(st.path, name, keys); err != nil {
			return err
		}
		stagesMu.Lock()
		active := stages[name] == st && !st.closed
		if active {
			st.keys = keys
		}
		stagesMu.Unlock()
		if !active {
			_ = os.Remove(st.path)
			return sameStage(name, st)
		}
		return nil
	}
	p := pinPath(name)
	if b, err := os.ReadFile(p); err == nil {
		if err := ownPin(name, p, b); err != nil {
			return err
		}
	}
	return writeKeys(p, name, keys)
}

// pinnedOther is the refusal of a key that differs from the pin that a
// saved host has: only host rekey replaces a pin.
func pinnedOther(name string, old []hostKey, shown string) error {
	was := "a pin that cannot be read"
	if len(old) > 0 {
		was = pinOf(old, "").Fingerprint
	}
	return &UserError{
		Msg:  fmt.Sprintf("%s has a pinned host key already, %s, and the host shows %s", name, was, shown),
		Hint: fmt.Sprintf("If you reinstalled %s: %s host rekey %s. Otherwise, do not connect.", name, brand.Name, name),
		Code: "host_key_pinned", Kind: KindRefused,
	}
}

// beforePinMigrationWrite schedules cancellation at the legacy migration boundary.
var (
	beforePinMigrationWrite = func(string) {}
	pinMigrationWritten     = func(string) {}
)
