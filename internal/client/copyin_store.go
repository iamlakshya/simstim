package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

const copyInStoreMaxBytes = 1 << 20 // 1 MiB

var copyInStoreMu sync.Mutex

func copyInStorePath() string { return filepath.Join(StateDir(), "copy-in.json") }

func copyInStoreKey(host, sessionID string) string { return host + "/" + sessionID }

// RememberCopyIn records a session's laptop-side --copy specs in StateDir so
// attach can re-upload them after a boot-time restore without ever trusting
// paths returned by a remote host (spec 17.3, G10).
func RememberCopyIn(host, sessionID string, specs []string) error {
	if host == "" || sessionID == "" || len(specs) == 0 {
		return nil
	}
	copyInStoreMu.Lock()
	defer copyInStoreMu.Unlock()

	records := loadCopyInRecordsLocked()
	records[copyInStoreKey(host, sessionID)] = slices.Clone(specs)
	return saveCopyInRecordsLocked(records)
}

// SavedCopyIn returns the laptop-recorded --copy specs for (host, sessionID),
// or nil when none were saved on this laptop.
func SavedCopyIn(host, sessionID string) []string {
	if host == "" || sessionID == "" {
		return nil
	}
	copyInStoreMu.Lock()
	defer copyInStoreMu.Unlock()

	records := loadCopyInRecordsLocked()
	return slices.Clone(records[copyInStoreKey(host, sessionID)])
}

// ForgetCopyIn removes any saved laptop-side --copy specs for (host, sessionID).
func ForgetCopyIn(host, sessionID string) {
	if host == "" || sessionID == "" {
		return
	}
	copyInStoreMu.Lock()
	defer copyInStoreMu.Unlock()

	records := loadCopyInRecordsLocked()
	key := copyInStoreKey(host, sessionID)
	if _, ok := records[key]; !ok {
		return
	}
	delete(records, key)
	_ = saveCopyInRecordsLocked(records)
}

func loadCopyInRecordsLocked() map[string][]string {
	path := copyInStorePath()
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > copyInStoreMaxBytes {
		return map[string][]string{}
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return map[string][]string{}
	}
	defer root.Close()
	b, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return map[string][]string{}
	}
	var records map[string][]string
	if err := json.Unmarshal(b, &records); err != nil || records == nil {
		return map[string][]string{}
	}
	return records
}

func saveCopyInRecordsLocked(records map[string][]string) error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state folder: %w", err)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("encode copy-in records: %w", err)
	}
	f, err := os.CreateTemp(dir, ".copy-in-*.tmp")
	if err != nil {
		return fmt.Errorf("create copy-in temp file: %w", err)
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
		return fmt.Errorf("chmod copy-in temp file: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write copy-in records: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close copy-in temp file: %w", err)
	}
	if err := os.Rename(tmp, copyInStorePath()); err != nil {
		return fmt.Errorf("save copy-in records: %w", err)
	}
	cleanup = false
	return nil
}
