package host

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidHostID reports whether id is 32 lower-case hex characters.
func ValidHostID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// HostID returns this host's ID, and creates it on first use.
//
// On first run, it writes 16 random bytes in hex to ~/.local/state/<name>/host-id
// with mode 0600, via a temporary file linked into place so concurrent first
// runs see one whole ID and never partial bytes. An existing file whose content
// is not 32 lower-case hex characters is an error, and is never replaced.
func HostID() (string, error) {
	placementProcesses.Lock()
	entry := placementProcesses.entry
	placementProcesses.Unlock()
	if entry != nil {
		return entry.hostID, nil
	}
	if err := CheckCaller(); err != nil {
		return "", err
	}
	p, err := placementForCall()
	if err != nil {
		return "", err
	}
	return p.hostID, nil
}

func readHostID(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat host ID: %w", err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	id := strings.TrimRight(string(b), "\r\n")
	if !ValidHostID(id) {
		return "", fmt.Errorf("%s does not hold a 32-character lower-case hex host ID", path)
	}
	return id, nil
}

func createHostID(dir, path string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate host ID: %w", err)
	}
	id := hex.EncodeToString(raw[:])

	tmp, err := os.CreateTemp(dir, ".host-id-*")
	if err != nil {
		return "", fmt.Errorf("make temporary host ID in %s: %w", filepath.Clean(dir), err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", tmpName, err)
	}

	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readHostID(path)
		}
		return "", fmt.Errorf("link %s: %w", path, err)
	}
	return id, nil
}
