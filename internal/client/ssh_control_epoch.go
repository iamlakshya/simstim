package client

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func controlToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// markControl publishes an epoch before returning SSH options. The host pin
// lock serialises command snapshots with forgetting and replacing the pin.
func markControl(dir, name, key string) string {
	_, scoped := controlScope()
	if !scoped || !ValidHostName(name) || !controlKeyRE.MatchString(key) || !privateDir(dir) {
		return ""
	}
	index := filepath.Join(dir, "h")
	if err := os.Mkdir(index, 0o700); err != nil && !privateDir(index) {
		return ""
	}
	if !privateDir(index) {
		return ""
	}
	hostDir := filepath.Join(index, controlHostKey(name))
	if err := os.Mkdir(hostDir, 0o700); err != nil && !privateDir(hostDir) {
		return ""
	}
	if !privateDir(hostDir) {
		return ""
	}
	marker := filepath.Join(hostDir, key)
	if token, ok := readControlMarker(marker); ok && token != "" {
		epoch := filepath.Join(dir, token)
		if privateDir(epoch) {
			return epoch
		}
		return "" // a missing epoch is retired, never recreate its old path
	}
	token := controlToken()
	if token == "" {
		return ""
	}
	epoch := filepath.Join(dir, token)
	if err := os.Mkdir(epoch, 0o700); err != nil {
		return ""
	}
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.Remove(epoch)
		if token, ok := readControlMarker(marker); ok && token != "" && privateDir(filepath.Join(dir, token)) {
			return filepath.Join(dir, token)
		}
		return ""
	}
	_, err = f.WriteString(token + "\n")
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(marker)
		_ = os.Remove(epoch)
		return ""
	}
	return epoch
}

func readControlMarker(path string) (string, bool) {
	if !controlMarkerValid(path) {
		return "", false
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", false
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return "", false
	}
	if len(data) == 0 {
		return "", true
	} // the pre-epoch index
	token, ok := strings.CutSuffix(string(data), "\n")
	return token, ok && controlKeyRE.MatchString(token)
}

// Rename first. An SSH operation paused before its bind or link cannot publish
// a listener at the prepared path after this point. Keep the private parent.
func retireControlEpoch(ctx context.Context, base, token string) error {
	if !controlKeyRE.MatchString(token) {
		return errors.New("invalid shared SSH epoch")
	}
	path := filepath.Join(base, token)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	if !privateDir(path) {
		return errors.New("the shared SSH folder is not owner-private")
	}
	retired := controlToken()
	if retired == "" {
		return errors.New("cannot name a retired SSH folder")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return fmt.Errorf("open the shared SSH base: %w", err)
	}
	defer root.Close()
	if err := root.Rename(token, retired); err != nil {
		return fmt.Errorf("retire the shared SSH folder: %w", err)
	}
	path = filepath.Join(base, retired)
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read the retired SSH folder: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return errors.New("unexpected folder in the shared SSH folder")
		}
		socket := filepath.Join(path, entry.Name())
		if controlSocketRE.MatchString(entry.Name()) {
			controlExit(ctx, socket)
		}
		if err := root.Remove(filepath.Join(retired, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove a retired SSH socket: %w", err)
		}
	}
	if err := root.Remove(retired); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the retired SSH folder: %w", err)
	}
	return nil
}

func retireControlIndex(ctx context.Context, dir, name string) error {
	index := filepath.Join(dir, "h", controlHostKey(name))
	if !privateDir(filepath.Dir(index)) || !privateDir(index) {
		return nil
	}
	entries, err := os.ReadDir(index)
	if err != nil {
		return fmt.Errorf("read shared SSH index: %w", err)
	}
	for _, entry := range entries {
		if !controlKeyRE.MatchString(entry.Name()) {
			continue
		}
		marker := filepath.Join(index, entry.Name())
		token, ok := readControlMarker(marker)
		if !ok {
			return errors.New("the shared SSH index cannot be read safely")
		}
		if token != "" {
			if err := retireControlEpoch(ctx, dir, token); err != nil {
				return err
			}
		} else {
			retireControlKey(ctx, dir, entry.Name())
		}
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove the shared SSH marker: %w", err)
		}
	}
	_ = os.Remove(index)
	return nil
}
