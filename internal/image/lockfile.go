package image

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/brand"
)

// LockVersion is the schema version written to images.lock (spec 20.8).
const LockVersion = 1

// LockFile holds laptop-side image pins (images.lock, spec 20.8).
type LockFile struct {
	Version int         `toml:"version"`
	Images  []LockEntry `toml:"image"`
}

// LockEntry pins one image reference to its manifest or index digest.
type LockEntry struct {
	Ref       string         `toml:"ref"`
	Digest    string         `toml:"digest"`
	PinnedAt  time.Time      `toml:"pinned_at"`
	SignedBy  string         `toml:"signed_by,omitempty"`
	Policy    string         `toml:"policy,omitempty"`
	HostID    string         `toml:"host_id,omitempty"`
	Platforms []LockPlatform `toml:"platform,omitempty"`
}

// LockPlatform records one architecture's platform digest and first conversion's
// dm-verity check value (root_hash + data_blocks).
type LockPlatform struct {
	Arch       string `toml:"arch"`
	Digest     string `toml:"digest"`
	RootHash   string `toml:"root_hash,omitempty"`
	DataBlocks uint64 `toml:"data_blocks,omitempty"`
	RootFrom   string `toml:"root_from,omitempty"`
	SignedBy   string `toml:"signed_by,omitempty"`
	Policy     string `toml:"policy,omitempty"`
}

// LoadLock reads images.lock from path, returning an empty version-1 lock file
// when path does not exist.
func LoadLock(path string) (LockFile, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return LockFile{Version: LockVersion}, nil
	}
	if err != nil {
		return LockFile{}, fmt.Errorf("read images.lock: %w", err)
	}
	var lf LockFile
	if err := toml.Unmarshal(b, &lf); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			if line, col := de.Position(); line > 0 {
				return LockFile{}, fmt.Errorf("decode images.lock at line %d, column %d: %s", line, col, strings.TrimPrefix(de.Error(), "toml: "))
			}
		}
		return LockFile{}, fmt.Errorf("decode images.lock: %w", err)
	}
	if lf.Version == 0 {
		lf.Version = LockVersion
	}
	return lf, nil
}

// Find returns the entry for ref, if pinned.
func (lf LockFile) Find(ref string) (LockEntry, bool) {
	for _, e := range lf.Images {
		if e.Ref == ref {
			return e, true
		}
	}
	return LockEntry{}, false
}

// Platform returns the recorded platform entry for arch, if present.
func (e LockEntry) Platform(arch string) (LockPlatform, bool) {
	for _, p := range e.Platforms {
		if p.Arch == arch {
			return p, true
		}
	}
	return LockPlatform{}, false
}

// Pin records ref -> digest in images.lock under an exclusive lock. If ref is
// already pinned to another digest and update is false, Pin returns an
// image_moved error without modifying the file (spec 20.8).
func Pin(path, ref, digest, host string, update bool) (entry LockEntry, created bool, prevDigest string, err error) {
	if _, err := ParseSHA256(digest); err != nil {
		return LockEntry{}, false, "", digestMismatch(ref, err.Error(), err)
	}
	unlock, err := lockFile(path+".lock", true)
	if err != nil {
		return LockEntry{}, false, "", err
	}
	defer unlock()

	lf, err := LoadLock(path)
	if err != nil {
		return LockEntry{}, false, "", err
	}
	now := time.Now().UTC()
	for i := range lf.Images {
		if lf.Images[i].Ref != ref {
			continue
		}
		if lf.Images[i].Digest == digest {
			return lf.Images[i], false, "", nil
		}
		if !update {
			h := host
			if h == "" {
				h = "<host>"
			}
			return LockEntry{}, false, lf.Images[i].Digest, &Error{
				Code: CodeMoved,
				Message: fmt.Sprintf("%s now points at %s, and images.lock pins %s",
					ref, shortDigest(digest), shortDigest(lf.Images[i].Digest)),
				Hint: fmt.Sprintf("%s image pull %s %s --update (after you check the new image)", brand.Name, ref, h),
			}
		}
		prevDigest = lf.Images[i].Digest
		lf.Images[i].Digest = digest
		lf.Images[i].PinnedAt = now
		lf.Images[i].Platforms = nil
		if err := saveLockLocked(path, lf); err != nil {
			return LockEntry{}, false, "", err
		}
		return lf.Images[i], false, prevDigest, nil
	}

	entry = LockEntry{
		Ref:      ref,
		Digest:   digest,
		PinnedAt: now,
	}
	lf.Images = append(lf.Images, entry)
	if err := saveLockLocked(path, lf); err != nil {
		return LockEntry{}, false, "", err
	}
	return entry, true, "", nil
}

// RecordPlatform saves a platform's conversion root_hash and data_blocks on its
// first conversion, or verifies that a later conversion matches the recorded
// check value (spec 20.2 step 6 & spec 20.8).
func RecordPlatform(path, ref, digest string, plat LockPlatform) error {
	unlock, err := lockFile(path+".lock", true)
	if err != nil {
		return err
	}
	defer unlock()

	lf, err := LoadLock(path)
	if err != nil {
		return err
	}
	for i := range lf.Images {
		if lf.Images[i].Ref != ref || lf.Images[i].Digest != digest {
			continue
		}
		for j := range lf.Images[i].Platforms {
			cur := &lf.Images[i].Platforms[j]
			if cur.Arch != plat.Arch {
				continue
			}
			if cur.RootHash != "" && plat.RootHash != "" && !strings.EqualFold(cur.RootHash, plat.RootHash) {
				return rootMismatchLock(ref, plat.RootHash, cur.RootHash)
			}
			if cur.RootHash == "" {
				cur.RootHash = plat.RootHash
				cur.DataBlocks = plat.DataBlocks
				cur.RootFrom = plat.RootFrom
			}
			if cur.Digest == "" {
				cur.Digest = plat.Digest
			}
			return saveLockLocked(path, lf)
		}
		lf.Images[i].Platforms = append(lf.Images[i].Platforms, plat)
		return saveLockLocked(path, lf)
	}
	return nil
}

func saveLockLocked(path string, lf LockFile) error {
	if lf.Version == 0 {
		lf.Version = LockVersion
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create images.lock folder: %w", err)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Written by %s. Each image that sandboxes may boot, pinned by digest.\n", brand.Name)
	if err := toml.NewEncoder(&buf).Encode(lf); err != nil {
		return fmt.Errorf("encode images.lock: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".images-*.lock")
	if err != nil {
		return fmt.Errorf("create temporary images.lock: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write images.lock: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync images.lock: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close images.lock: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install images.lock: %w", err)
	}
	return nil
}
