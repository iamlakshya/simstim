package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/iamlakshya/simstim/internal/api"
)

// Store garbage-collection windows from spec 20.2.
const (
	pruneKeepUsed    = 7 * 24 * time.Hour
	pruneKeepArrived = time.Hour
)

// Store manages converted read-only ext4 session images and agents disks under
// one content-addressed host directory (paths.ImagesDir()).
type Store struct {
	Dir    string
	Logger *slog.Logger
	Now    func() time.Time
}

// NewStore opens or creates an image store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// PullRequest asks Store.Pull to ensure a digest-pinned OCI image is stored.
type PullRequest struct {
	Ref              string
	Digest           string
	Arch             string
	ExpectedRootHash string
	PullOptions      PullOptions
	Limits           Limits
}

// PruneKeep describes which stored images and agents disks Prune must retain.
type PruneKeep struct {
	// SessionImages are platform digest hexes (or sha256:<hex>) named by
	// sessions that are not deleted.
	SessionImages []string
	// DefaultImages are platform digest hexes (or sha256:<hex>) pinned as the
	// default session image.
	DefaultImages []string
	// SessionAgents are agents-disk SHA-256 hexes named by non-deleted sessions.
	SessionAgents []string
}

// ImagePath returns the rootfs.img path for a validated 64-hex platform digest.
func (s *Store) ImagePath(hexDigest string) string {
	return filepath.Join(s.Dir, hexDigest, "rootfs.img")
}

func (s *Store) recordPath(hexDigest string) string {
	return filepath.Join(s.Dir, hexDigest, "image.json")
}

func (s *Store) lockPath(hexDigest string) string {
	return filepath.Join(s.Dir, hexDigest+".lock")
}

// List returns every stored image record, newest used/converted first.
func (s *Store) List() ([]api.ImageRecord, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read image store: %w", err)
	}
	var out []api.ImageRecord
	for _, e := range entries {
		if !e.IsDir() || !isHex64(e.Name()) {
			continue
		}
		rec, err := s.readRecord(e.Name())
		if err != nil {
			continue
		}
		if _, err := os.Stat(s.ImagePath(e.Name())); err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UsedAt.Equal(out[j].UsedAt) {
			return out[i].UsedAt.After(out[j].UsedAt)
		}
		if !out[i].ConvertedAt.Equal(out[j].ConvertedAt) {
			return out[i].ConvertedAt.After(out[j].ConvertedAt)
		}
		return out[i].PlatformDigest < out[j].PlatformDigest
	})
	return out, nil
}

// Get finds a stored image by full digest ("sha256:<hex>"), hex, or an
// unambiguous hex prefix, matching either PlatformDigest or Digest.
func (s *Store) Get(query string) (api.ImageRecord, string, error) {
	q := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query), "sha256:"))
	if q == "" {
		return api.ImageRecord{}, "", &Error{
			Code:    CodeNotFound,
			Message: "image digest is empty",
			Hint:    "Run `image ls` to see stored images.",
		}
	}
	list, err := s.List()
	if err != nil {
		return api.ImageRecord{}, "", err
	}
	var matches []api.ImageRecord
	for _, rec := range list {
		pHex := strings.TrimPrefix(rec.PlatformDigest, "sha256:")
		dHex := strings.TrimPrefix(rec.Digest, "sha256:")
		if pHex == q || dHex == q {
			return rec, s.ImagePath(pHex), nil
		}
		if strings.HasPrefix(pHex, q) || strings.HasPrefix(dHex, q) {
			matches = append(matches, rec)
		}
	}
	if len(matches) == 1 {
		pHex := strings.TrimPrefix(matches[0].PlatformDigest, "sha256:")
		return matches[0], s.ImagePath(pHex), nil
	}
	if len(matches) > 1 {
		return api.ImageRecord{}, "", &Error{
			Code:    CodeNotFound,
			Message: fmt.Sprintf("digest prefix %q matches %d images", query, len(matches)),
			Hint:    "Pass more characters of the digest.",
		}
	}
	return api.ImageRecord{}, "", &Error{
		Code:    CodeNotFound,
		Message: fmt.Sprintf("no stored image matches %q", query),
		Hint:    "List stored images with `image ls`.",
	}
}

// Pull fetches a digest-pinned OCI image via go-containerregistry, flattens and
// converts it into the store, and returns its record. Concurrent pulls of the
// same digest wait on the per-digest lock and reuse the completed image.
func (s *Store) Pull(ctx context.Context, req PullRequest) (api.ImageRecord, error) {
	arch := req.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	ref, pinnedDefault, isDefault := ResolveDefault(req.Ref)
	digest := req.Digest
	if digest == "" && isDefault {
		digest = pinnedDefault
	}
	if _, err := ParseSHA256(digest); err != nil {
		return api.ImageRecord{}, digestMismatch(ref, err.Error(), err)
	}
	expectedRoot := req.ExpectedRootHash
	if expectedRoot == "" && isDefault {
		if dp, err := DefaultForArch(arch); err == nil {
			expectedRoot = dp.RootHash
		}
	}

	// Fast path: reuse an already-stored image under its per-digest lock.
	if rec, ok, err := s.tryReuseStored(ref, digest, arch, expectedRoot); err != nil {
		return api.ImageRecord{}, err
	} else if ok {
		return rec, nil
	}

	img, platDigest, err := Fetch(ctx, ref, digest, arch, req.PullOptions)
	if err != nil {
		return api.ImageRecord{}, err
	}
	return s.InstallImage(ref, digest, platDigest, arch, expectedRoot, img, req.Limits)
}

func (s *Store) tryReuseStored(ref, digest, arch, expectedRoot string) (api.ImageRecord, bool, error) {
	rec, ok, err := s.findStored(digest, arch)
	if err != nil || !ok {
		return api.ImageRecord{}, false, err
	}
	platHex := strings.TrimPrefix(rec.PlatformDigest, "sha256:")
	unlock, err := lockFile(s.lockPath(platHex), true)
	if err != nil {
		return api.ImageRecord{}, false, err
	}
	defer unlock()

	cur, err := s.readRecord(platHex)
	if err != nil {
		return api.ImageRecord{}, false, nil
	}
	if _, serr := os.Stat(s.ImagePath(platHex)); serr != nil {
		return api.ImageRecord{}, false, nil
	}
	if cur.Arch != arch || (cur.Digest != digest && cur.PlatformDigest != digest) {
		return api.ImageRecord{}, false, nil
	}
	if expectedRoot != "" && !strings.EqualFold(cur.RootHash, expectedRoot) {
		return api.ImageRecord{}, false, rootMismatchStored(ref, cur.RootHash, expectedRoot)
	}
	cur.UsedAt = s.now()
	if err := s.writeRecord(platHex, cur); err != nil {
		return api.ImageRecord{}, false, err
	}
	return cur, true, nil
}

// InstallImage converts an already-loaded v1.Image into the content-addressed
// store under its platform digest lock.
func (s *Store) InstallImage(ref, digest, platformDigest, arch, expectedRootHash string, img v1.Image, lim Limits) (api.ImageRecord, error) {
	platHex, err := ParseSHA256(platformDigest)
	if err != nil {
		return api.ImageRecord{}, digestMismatch(ref, err.Error(), err)
	}
	if digest == "" {
		digest = platformDigest
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return api.ImageRecord{}, fmt.Errorf("create image store: %w", err)
	}

	unlock, err := lockFile(s.lockPath(platHex), true)
	if err != nil {
		return api.ImageRecord{}, err
	}
	defer unlock()

	// Re-check under the exclusive lock: a concurrent pull may have finished.
	if rec, err := s.readRecord(platHex); err == nil {
		if _, serr := os.Stat(s.ImagePath(platHex)); serr == nil {
			if expectedRootHash != "" && !strings.EqualFold(rec.RootHash, expectedRootHash) {
				return api.ImageRecord{}, rootMismatchStored(ref, rec.RootHash, expectedRootHash)
			}
			rec.UsedAt = s.now()
			_ = s.writeRecord(platHex, rec)
			return rec, nil
		}
	}

	imgDir := filepath.Join(s.Dir, platHex)
	if err := os.MkdirAll(imgDir, 0o700); err != nil {
		return api.ImageRecord{}, fmt.Errorf("create image folder: %w", err)
	}
	conv, err := ConvertImage(img, ref, s.ImagePath(platHex), ConvertOptions{
		Limits:           lim,
		ExpectedRootHash: expectedRootHash,
		Logger:           s.Logger,
	})
	if err != nil {
		_ = os.Remove(imgDir)
		return api.ImageRecord{}, err
	}

	now := s.now()
	rec := api.ImageRecord{
		Ref:            ref,
		Digest:         digest,
		PlatformDigest: platformDigest,
		Arch:           arch,
		RootHash:       conv.RootHash,
		DataBlocks:     conv.DataBlocks,
		Size:           conv.Size,
		ConvertedAt:    now,
		UsedAt:         now,
	}
	if err := s.writeRecord(platHex, rec); err != nil {
		_ = os.RemoveAll(imgDir)
		return api.ImageRecord{}, err
	}
	return rec, nil
}

// Touch marks a stored image as used now, under a shared check / exclusive write.
func (s *Store) Touch(digestOrHex string) error {
	rec, _, err := s.Get(digestOrHex)
	if err != nil {
		return err
	}
	platHex := strings.TrimPrefix(rec.PlatformDigest, "sha256:")
	unlock, err := lockFile(s.lockPath(platHex), true)
	if err != nil {
		return err
	}
	defer unlock()
	rec, err = s.readRecord(platHex)
	if err != nil {
		return err
	}
	rec.UsedAt = s.now()
	return s.writeRecord(platHex, rec)
}

// Remove deletes one stored image unless inUse reports a session that still
// references its digest.
func (s *Store) Remove(query string, inUse func(rec api.ImageRecord) string) (api.ImageRecord, error) {
	rec, _, err := s.Get(query)
	if err != nil {
		return api.ImageRecord{}, err
	}
	platHex := strings.TrimPrefix(rec.PlatformDigest, "sha256:")
	unlock, err := lockFile(s.lockPath(platHex), true)
	if err != nil {
		return api.ImageRecord{}, err
	}
	defer unlock()

	if inUse != nil {
		if sess := inUse(rec); sess != "" {
			return api.ImageRecord{}, imageInUse(rec.PlatformDigest, sess)
		}
	}
	if err := os.RemoveAll(filepath.Join(s.Dir, platHex)); err != nil {
		return api.ImageRecord{}, fmt.Errorf("remove image %s: %w", platHex, err)
	}
	return rec, nil
}

// Prune deletes stored images and old agents disks not retained by keep or the
// spec 20.2 retention windows (7 days used/converted, 1 hour arrived). It
// returns the digests of removed images.
func (s *Store) Prune(keep PruneKeep) ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read image store: %w", err)
	}

	pinned := make(map[string]bool)
	for _, list := range [][]string{keep.SessionImages, keep.DefaultImages} {
		for _, d := range list {
			h := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "sha256:"))
			if h != "" {
				pinned[h] = true
			}
		}
	}

	now := s.now()
	var removed []string
	for _, e := range entries {
		if !e.IsDir() || !isHex64(e.Name()) {
			continue
		}
		hexName := e.Name()
		if pinned[hexName] {
			continue
		}
		unlock, lerr := lockFile(s.lockPath(hexName), true)
		if lerr != nil {
			continue
		}
		shouldDelete := s.canPruneLocked(hexName, pinned, now)
		if shouldDelete {
			if rerr := os.RemoveAll(filepath.Join(s.Dir, hexName)); rerr == nil {
				removed = append(removed, "sha256:"+hexName)
			}
		}
		unlock()
	}

	agentKeep := make(map[string]bool)
	for _, d := range keep.SessionAgents {
		h := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "sha256:"))
		if h != "" {
			agentKeep[h] = true
		}
	}
	if _, aerr := s.PruneAgentsDisks(agentKeep); aerr != nil && !errors.Is(aerr, os.ErrNotExist) {
		return removed, aerr
	}
	sort.Strings(removed)
	return removed, nil
}

func (s *Store) canPruneLocked(hexName string, pinned map[string]bool, now time.Time) bool {
	if pinned[hexName] {
		return false
	}
	rec, err := s.readRecord(hexName)
	if err != nil {
		// Partial or broken directory: keep for 1 hour in case a pull just wrote it.
		if fi, serr := os.Stat(filepath.Join(s.Dir, hexName)); serr == nil && now.Sub(fi.ModTime()) < pruneKeepArrived {
			return false
		}
		return true
	}
	dHex := strings.ToLower(strings.TrimPrefix(rec.Digest, "sha256:"))
	if pinned[dHex] {
		return false
	}
	if !rec.UsedAt.IsZero() && now.Sub(rec.UsedAt) < pruneKeepUsed {
		return false
	}
	if !rec.ConvertedAt.IsZero() && now.Sub(rec.ConvertedAt) < pruneKeepUsed {
		return false
	}
	if fi, serr := os.Stat(s.ImagePath(hexName)); serr == nil && now.Sub(fi.ModTime()) < pruneKeepArrived {
		return false
	}
	return true
}

func (s *Store) findStored(digest, arch string) (api.ImageRecord, bool, error) {
	list, err := s.List()
	if err != nil {
		return api.ImageRecord{}, false, err
	}
	for _, rec := range list {
		if rec.Arch == arch && (rec.Digest == digest || rec.PlatformDigest == digest) {
			return rec, true, nil
		}
	}
	return api.ImageRecord{}, false, nil
}

func (s *Store) readRecord(hexDigest string) (api.ImageRecord, error) {
	b, err := os.ReadFile(s.recordPath(hexDigest))
	if err != nil {
		return api.ImageRecord{}, fmt.Errorf("read image record: %w", err)
	}
	var rec api.ImageRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return api.ImageRecord{}, fmt.Errorf("decode image record: %w", err)
	}
	return rec, nil
}

func (s *Store) writeRecord(hexDigest string, rec api.ImageRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode image record: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Join(s.Dir, hexDigest)
	tmp, err := os.CreateTemp(dir, ".image-*.json")
	if err != nil {
		return fmt.Errorf("write image record: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write image record: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync image record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close image record: %w", err)
	}
	if err := os.Rename(tmpPath, s.recordPath(hexDigest)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("install image record: %w", err)
	}
	return nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
