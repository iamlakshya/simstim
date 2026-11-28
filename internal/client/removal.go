package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

const hostsLockWait = 10 * time.Second

// HostStateDir is ~/.local/state/<name>/hosts/<lower>-<hash>/, which holds the
// per-host lock and removal record. Hashing the exact name keeps case twins
// separate on case-insensitive file systems (cli-spec 11.3, rule 10).
func HostStateDir(name string) string {
	sum := sha256.Sum256([]byte(name))
	slug := strings.ToLower(name) + "-" + hex.EncodeToString(sum[:4])
	return filepath.Join(StateDir(), "hosts", slug)
}

// RemovalRecordPath is the path to a host's removal record.
func RemovalRecordPath(name string) string {
	return filepath.Join(HostStateDir(name), "removal.json")
}

// RemovalRecord records completed parts of a `host rm` run so a retry after a
// failure knows what was already deleted (cli-spec 11.3, rule 10).
type RemovalRecord struct {
	Host           string    `json:"host"`
	Provider       string    `json:"provider,omitempty"`
	MachineID      string    `json:"machine_id,omitempty"`
	MachineCreated string    `json:"machine_created,omitempty"`
	Destroyed      bool      `json:"destroyed,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// LoadRemovalRecord reads a host's removal record. It returns (zero, false, nil)
// when no record exists.
func LoadRemovalRecord(name string) (RemovalRecord, bool, error) {
	p := RemovalRecordPath(name)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return RemovalRecord{}, false, nil
	}
	if err != nil {
		return RemovalRecord{}, false, fmt.Errorf("read %s: %w", p, err)
	}
	var rec RemovalRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return RemovalRecord{}, false, fmt.Errorf("read %s: %w", p, err)
	}
	return rec, true, nil
}

// SaveRemovalRecord writes a host's removal record atomically with mode 0600.
func SaveRemovalRecord(rec RemovalRecord) error {
	dir := HostStateDir(rec.Host)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return lockDirError(dir, err)
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = time.Now().UTC()
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode removal record: %w", err)
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(dir, "removal.json.tmp-*")
	if err != nil {
		return fmt.Errorf("write removal record: %w", err)
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("write removal record: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("write removal record: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write removal record: %w", err)
	}
	if err := os.Rename(tmp, RemovalRecordPath(rec.Host)); err != nil {
		return fmt.Errorf("write removal record: %w", err)
	}
	cleanup = false
	return nil
}

// DeleteRemovalRecord removes a host's removal record if it exists. If removal
// fails, it returns a UserError naming the file path (cli-spec 11.3, rule 10).
func DeleteRemovalRecord(name string) error {
	p := RemovalRecordPath(name)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &UserError{
			Msg:  fmt.Sprintf("cannot delete removal record %s: %v", p, err),
			Hint: fmt.Sprintf("Remove %s and run the command again.", p),
			Code: "removal_record",
		}
	}
	return nil
}

// LockHost takes an exclusive non-blocking lock on name for the command op
// (such as "host rm" or "host set"). If another command holds the lock, it
// fails at once with host_busy (cli-spec 11.3, rule 13).
func LockHost(ctx context.Context, name, op string) (unlock func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, stoppedBefore("lock "+name, err)
	}
	dir := HostStateDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, lockDirError(dir, err)
	}
	path := filepath.Join(dir, "lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, lockDirError(path, err)
	}
	ok, err := tryLockFile(f, true)
	if err != nil {
		_ = f.Close()
		return nil, &UserError{
			Msg:  fmt.Sprintf("cannot lock %s: %v", path, err),
			Hint: "Keep the state folder on a local disk that supports file locks.",
			Code: "lock_failed",
		}
	}
	if !ok {
		holder := readLockHolder(f)
		_ = f.Close()
		msg := name + " is busy: another command changes it"
		if holder != "" {
			msg = fmt.Sprintf("%s is busy: %s changes it", name, holder)
		}
		return nil, &UserError{
			Msg:  msg,
			Hint: "Wait for the other command to finish, then try again.",
			Code: "host_busy",
		}
	}
	if err := ctx.Err(); err != nil {
		_ = unlockFile(f)
		_ = f.Close()
		return nil, stoppedBefore("lock "+name, err)
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = f.WriteString(op + "\n")
	return func() {
		_ = f.Truncate(0)
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}

func readLockHolder(f *os.File) string {
	buf := make([]byte, 128)
	n, _ := f.ReadAt(buf, 0)
	return CleanLine(strings.TrimSpace(string(buf[:n])))
}

func lockDirError(target string, err error) error {
	cur := target
	for {
		fi, statErr := os.Stat(cur)
		if statErr == nil {
			if !fi.IsDir() {
				return &UserError{
					Msg:  fmt.Sprintf("cannot make lock %s: %s is a file", target, cur),
					Hint: fmt.Sprintf("Move or remove %s, then try again.", cur),
					Code: "lock_failed",
				}
			}
			return &UserError{
				Msg:  fmt.Sprintf("cannot make lock %s: %v", target, err),
				Hint: fmt.Sprintf("Make %s writable, then try again.", cur),
				Code: "lock_failed",
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return &UserError{
				Msg:  fmt.Sprintf("cannot make lock %s: %v", target, err),
				Hint: fmt.Sprintf("Make %s writable, then try again.", filepath.Dir(target)),
				Code: "lock_failed",
			}
		}
		cur = parent
	}
}

// lockHostsFile takes the short exclusive lock on hosts.toml so concurrent
// commands on different hosts never lose each other's writes.
func lockHostsFile(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, stoppedBefore("save the hosts", err)
	}
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, lockDirError(dir, err)
	}
	path := hostsPath() + ".lock"
	unlock, err := lockPathContext(ctx, path, hostsLockWait, true)
	if err == nil {
		return unlock, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, stoppedBefore("save the hosts", err)
	}
	if errors.Is(err, errLockBusy) {
		return nil, &UserError{
			Msg:  "another command is saving hosts.toml",
			Hint: "Wait for the other command to finish, then try again.",
			Code: "hosts_busy",
		}
	}
	return nil, lockDirError(path, err)
}

// HostsEqual reports whether two Host entries have the same fields, treating
// empty and nil slices or maps as equal.
func HostsEqual(a, b Host) bool {
	if len(a.Labels) == 0 {
		a.Labels = nil
	}
	if len(b.Labels) == 0 {
		b.Labels = nil
	}
	if len(a.Secrets) == 0 {
		a.Secrets = nil
	}
	if len(b.Secrets) == 0 {
		b.Secrets = nil
	}
	return reflect.DeepEqual(a, b)
}

// SaveHostIfUnchanged saves next only while the saved entry for next.Name
// still matches prev (or does not exist when prev is nil). Otherwise nothing
// is written and host_changed or host_exists is returned (cli-spec 11.3, rule 13).
func SaveHostIfUnchanged(ctx context.Context, prev *Host, next Host) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("save the hosts", err)
	}
	unlock, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	idx := -1
	for i := range hosts {
		if hosts[i].Name == next.Name {
			idx = i
			break
		}
	}
	if prev == nil {
		if idx >= 0 {
			return &UserError{
				Msg:  "there is already a host called " + next.Name,
				Hint: brand.Name + " host show " + ShellQuote(next.Name),
				Code: "host_exists",
			}
		}
		return lockedSaveHosts(ctx, append(hosts, next))
	}
	if idx < 0 || !HostsEqual(*prev, hosts[idx]) {
		return &UserError{
			Msg:  next.Name + " changed while the command ran, so it was not saved",
			Hint: brand.Name + " host show " + ShellQuote(next.Name) + " (then run the command again)",
			Code: "host_changed",
		}
	}
	hosts[idx] = next
	return lockedSaveHosts(ctx, hosts)
}

// ForgetHostIfUnchanged removes prev from hosts.toml only while the saved
// entry still matches prev, removing its generated SSH config first.
func ForgetHostIfUnchanged(ctx context.Context, prev Host) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("forget "+prev.Name, err)
	}
	unlock, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	idx := -1
	for i := range hosts {
		if hosts[i].Name == prev.Name {
			idx = i
			break
		}
	}
	if idx < 0 || !HostsEqual(prev, hosts[idx]) {
		return &UserError{
			Msg:  prev.Name + " changed while the command ran, so it was not forgotten",
			Hint: brand.Name + " host show " + ShellQuote(prev.Name) + " (then run the command again)",
			Code: "host_changed",
		}
	}
	if err := RemoveWrapperConfig(prev); err != nil {
		return err
	}
	hosts = append(hosts[:idx], hosts[idx+1:]...)
	if err := lockedSaveHosts(ctx, hosts); err != nil {
		return err
	}
	_ = DeleteRemovalRecord(prev.Name)
	return CleanupHostFiles(ctx, prev)
}
