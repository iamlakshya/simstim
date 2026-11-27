package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/brand"
)

type tiersFile struct {
	Migrated time.Time      `toml:"migrated,omitempty"`
	Hosts    []tierFileHost `toml:"host"`
}

type tierFileHost struct {
	Name        string    `toml:"name"`
	Tier        string    `toml:"tier"`
	HostID      string    `toml:"host_id,omitempty"`
	Key         string    `toml:"key,omitempty"`
	SetAt       time.Time `toml:"set_at,omitempty"`
	IDChanged   bool      `toml:"id_changed,omitempty"`
	NeedsAnswer bool      `toml:"needs_answer,omitempty"`
}

func tiersPath() string { return filepath.Join(ConfigDir(), "tiers.toml") }

func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Second) }

// beforeTierMigrationSave runs before the final cancellation check. Tests
// cancel here to prove that a migration never saves after cancellation.
var beforeTierMigrationSave = func() {}

func loadTiers(ctx context.Context, hosts []Host) (Tiers, error) {
	if err := ctx.Err(); err != nil {
		return Tiers{}, err
	}
	ts, err := readTiersFile()
	if err == nil {
		return ts, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Tiers{}, err
	}
	for _, h := range hosts {
		if err := ctx.Err(); err != nil {
			return Tiers{}, err
		}
		if err := migratePinContext(ctx, h.Name); err != nil {
			return Tiers{}, err
		}
	}
	unlockHosts, err := lockHostsFile(ctx)
	if err != nil {
		return Tiers{}, err
	}
	defer unlockHosts()
	hosts, err = LoadHosts()
	if err != nil {
		return Tiers{}, err
	}
	// Membership stays fixed until migration commits. Read the pin files
	// before taking the tiers lock and never acquire a pin lock here.
	migrated := buildMigratedTiers(hosts)
	unlock, err := lockTiersFile(ctx)
	if err != nil {
		return Tiers{}, err
	}
	defer unlock()
	if ts, err := readTiersFile(); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ts, err
	}
	ts = migrated
	beforeTierMigrationSave()
	if err := ctx.Err(); err != nil {
		return Tiers{}, err
	}
	if err := writeTiersFile(ts); err != nil {
		return Tiers{}, err
	}
	return ts, nil
}

func ensureTiersMigrated(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(tiersPath()); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat tiers file: %w", err)
	}
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	_, err = loadTiers(ctx, hosts)
	return err
}

func buildMigratedTiers(hosts []Host) Tiers {
	at := nowUTC()
	entries := make(map[string]TierEntry, len(hosts))
	for _, h := range hosts {
		if !ValidHostName(h.Name) {
			continue
		}
		var key string
		if p, err := PinOf(h.Name); err == nil {
			key = p.Fingerprint
		}
		entries[h.Name] = TierEntry{
			Name:  h.Name,
			Tier:  TierOwn,
			Key:   key,
			SetAt: at,
		}
	}
	return Tiers{Migrated: at, entries: entries}
}

func readTiersFile() (Tiers, error) {
	p := tiersPath()
	st, err := os.Lstat(p)
	if err != nil {
		return Tiers{}, fmt.Errorf("stat tiers file: %w", err)
	}
	if !st.Mode().IsRegular() {
		return Tiers{}, &UserError{
			Msg:  p + " is not a regular file",
			Hint: "Check " + p + ".",
			Code: "bad_config",
			Kind: KindUsage,
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Tiers{}, fmt.Errorf("read %s: %w", p, err)
	}
	var raw tiersFile
	if _, err := toml.Decode(string(b), &raw); err != nil {
		return Tiers{}, tomlError(p, string(b), err)
	}
	entries := make(map[string]TierEntry, len(raw.Hosts))
	for _, h := range raw.Hosts {
		if !ValidHostName(h.Name) {
			return Tiers{}, &UserError{
				Msg:  fmt.Sprintf("%s: %q cannot name a host", p, CleanLine(h.Name)),
				Hint: "Fix " + p + ".",
				Code: "bad_config",
				Kind: KindUsage,
			}
		}
		tier, err := ParseTier(h.Tier)
		if err != nil {
			return Tiers{}, &UserError{
				Msg:  fmt.Sprintf("%s: %q is not a host tier for %s", p, CleanLine(h.Tier), h.Name),
				Hint: "Fix " + p + ": a tier is own, throwaway or shared.",
				Code: "bad_config",
				Kind: KindUsage,
			}
		}
		if h.HostID != "" && !ValidHostID(h.HostID) {
			return Tiers{}, &UserError{
				Msg:  fmt.Sprintf("%s: %s has an invalid host_id", p, h.Name),
				Hint: "Fix " + p + ": host_id is 32 lower-case hex characters.",
				Code: "bad_config",
				Kind: KindUsage,
			}
		}
		if err := checkFingerprint(h.Key); err != nil {
			return Tiers{}, &UserError{
				Msg:  fmt.Sprintf("%s: %s has an invalid key fingerprint", p, h.Name),
				Hint: "Fix " + p + ".",
				Code: "bad_config",
				Kind: KindUsage,
			}
		}
		entries[h.Name] = TierEntry{
			Name:        h.Name,
			Tier:        tier,
			HostID:      h.HostID,
			Key:         h.Key,
			SetAt:       h.SetAt,
			IDChanged:   h.IDChanged,
			NeedsAnswer: h.NeedsAnswer,
		}
	}
	return Tiers{Migrated: raw.Migrated, entries: entries}, nil
}

func writeTiersFile(ts Tiers) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	raw := tiersFile{
		Migrated: ts.Migrated,
		Hosts:    make([]tierFileHost, 0, len(ts.entries)),
	}
	for _, e := range ts.entries {
		if !ValidHostName(e.Name) || !e.Tier.Valid() {
			continue
		}
		raw.Hosts = append(raw.Hosts, tierFileHost{
			Name:        e.Name,
			Tier:        e.Tier.String(),
			HostID:      e.HostID,
			Key:         e.Key,
			SetAt:       e.SetAt,
			IDChanged:   e.IDChanged,
			NeedsAnswer: e.NeedsAnswer,
		})
	}
	sort.Slice(raw.Hosts, func(i, j int) bool { return raw.Hosts[i].Name < raw.Hosts[j].Name })

	f, err := os.CreateTemp(dir, ".tiers-*")
	if err != nil {
		return fmt.Errorf("save %s: %w", tiersPath(), err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("save %s: %w", tiersPath(), err)
	}
	fmt.Fprintf(f, "# Tiers of %s hosts. Edit with `%s host set <host> tier=<tier>`.\n\n", brand.DisplayName, brand.Name)
	if err := toml.NewEncoder(f).Encode(raw); err != nil {
		_ = f.Close()
		return fmt.Errorf("save %s: %w", tiersPath(), err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("save %s: %w", tiersPath(), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save %s: %w", tiersPath(), err)
	}
	if err := os.Rename(tmp, tiersPath()); err != nil {
		return fmt.Errorf("save %s: %w", tiersPath(), err)
	}
	return nil
}

// lockTiersFile takes the lock of tiers.toml. In the lock order it comes
// last: LockHost -> lockPin -> lockHostsFile -> lockTiersFile. While held,
// no caller acquires an earlier lock.
func lockTiersFile(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := tiersPath() + ".lock"
	f, err := openLock(path, "tiers.toml")
	if err != nil {
		return nil, err
	}
	start, noted := time.Now(), false
	for {
		err := takeLock(f)
		switch {
		case err == nil && ctx.Err() != nil:
			_ = f.Close()
			return nil, ctx.Err()
		case err == nil:
			return func() { _ = f.Close() }, nil
		case !errors.Is(err, errLockTaken):
			_ = f.Close()
			return nil, flockFailed("tiers.toml", path, err)
		case time.Since(start) >= hostsWait:
			_ = f.Close()
			return nil, &UserError{
				Msg:  fmt.Sprintf("another command held %s for %s, so nothing was saved", tiersPath(), hostsWait),
				Hint: "End that command, such as one stopped with Ctrl+Z, then run this one again.",
				Code: "hosts_busy",
			}
		case !noted && time.Since(start) >= hostsNote:
			noted = true
			if w := notesOf(ctx); w != nil {
				fmt.Fprintf(w, "Waiting for another command that saves %s…\n", tiersPath())
			}
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(hostsPoll):
		}
	}
}

func mutateTiers(ctx context.Context, hosts []Host, fn func(*Tiers) error) error {
	_, err := mutateTiersWithRollback(ctx, hosts, fn)
	return err
}

func mutateTiersWithRollback(ctx context.Context, hosts []Host, fn func(*Tiers) error) (func(), error) {
	unlock, err := lockTiersFile(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ts, err := readTiersFile()
	if errors.Is(err, os.ErrNotExist) {
		ts = buildMigratedTiers(hosts)
		err = nil
	}
	if err != nil {
		return nil, err
	}
	before := canonicalTierEntries(ts)
	if ts.entries == nil {
		ts.entries = map[string]TierEntry{}
	}
	if err := fn(&ts); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeTiersFile(ts); err != nil {
		return nil, err
	}
	return tierRollback(ctx, before, canonicalTierEntries(ts)), nil
}
