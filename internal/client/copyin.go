package client

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
)

// DefaultCopyInMaxBytes caps the total bytes copied by copy_in / --copy at
// one session start (64 MiB, spec 17.3).
const DefaultCopyInMaxBytes int64 = 64 << 20

// DefaultCopyInMaxEntries caps the total files and folders copied by
// copy_in / --copy at one session start (10,000, spec 17.3).
const DefaultCopyInMaxEntries = 10_000

// CopyInItem is one resolved laptop-to-guest file or folder copy.
type CopyInItem struct {
	// Source is the original spec string (recorded on the manifest so restore
	// can re-send it on the next attach).
	Source string
	// LocalPath is the expanded absolute path on this laptop.
	LocalPath string
	// GuestPath is the normalized absolute destination inside the session
	// (with leading ~/ expanded to /home/agent/).
	GuestPath string
}

// ParseCopyInSpecs parses copy_in config entries and --copy flags into
// normalized CopyInItems, mapping home-relative paths under /home/agent,
// non-home paths under /mnt/<base>, and refusing guest path collisions between
// distinct laptop paths with exit code 2 (spec 17.3).
func ParseCopyInSpecs(specs []string, homeDir string) ([]CopyInItem, error) {
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	if homeDir != "" {
		if abs, err := filepath.Abs(homeDir); err == nil {
			homeDir = abs
		}
	}
	var items []CopyInItem
	byGuest := map[string]string{}
	for _, raw := range specs {
		item, err := parseOneCopyInSpec(raw, homeDir)
		if err != nil {
			return nil, err
		}
		if prev, exists := byGuest[item.GuestPath]; exists {
			if prev == item.LocalPath {
				continue // duplicate of identical laptop -> guest mapping
			}
			return nil, &UserError{
				Msg:  fmt.Sprintf("two --copy entries target the same guest path %s: %s and %s", item.GuestPath, CleanPath(prev), CleanPath(item.LocalPath)),
				Hint: "Give one of them a different destination with --copy <laptop>:<guest>.",
				Code: "copy_conflict",
				Kind: KindUsage,
			}
		}
		byGuest[item.GuestPath] = item.LocalPath
		items = append(items, item)
	}
	return items, nil
}

func parseOneCopyInSpec(raw, homeDir string) (CopyInItem, error) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return CopyInItem{}, &UserError{
			Msg:  "empty --copy path",
			Hint: "Pass --copy <laptop path> or --copy <laptop path>:<guest path>.",
			Code: "bad_value",
			Kind: KindUsage,
		}
	}
	localRaw, guestRaw, hasGuest := splitCopySpec(spec)
	if localRaw == "" {
		return CopyInItem{}, &UserError{
			Msg:  fmt.Sprintf("invalid --copy value %q: missing laptop path", CleanLine(spec)),
			Hint: "Pass --copy <laptop path>[:<guest path>].",
			Code: "bad_value",
			Kind: KindUsage,
		}
	}
	localExpanded, err := expandLaptopPath(localRaw, homeDir)
	if err != nil {
		return CopyInItem{}, err
	}
	var guestPath string
	if hasGuest {
		guestPath, err = normalizeGuestCopyPath(guestRaw)
		if err != nil {
			return CopyInItem{}, err
		}
	} else {
		guestPath = defaultGuestCopyPath(localExpanded, homeDir)
	}
	return CopyInItem{
		Source:    spec,
		LocalPath: localExpanded,
		GuestPath: guestPath,
	}, nil
}

// splitCopySpec splits `<laptop>[:<guest>]`, preserving a leading Windows drive
// prefix such as `C:\path`.
func splitCopySpec(spec string) (localPart, guestPart string, hasGuest bool) {
	start := 0
	if len(spec) >= 2 && spec[1] == ':' && ((spec[0] >= 'a' && spec[0] <= 'z') || (spec[0] >= 'A' && spec[0] <= 'Z')) {
		start = 2
	}
	idx := strings.IndexByte(spec[start:], ':')
	if idx < 0 {
		return spec, "", false
	}
	pos := start + idx
	return spec[:pos], spec[pos+1:], true
}

func expandLaptopPath(p, homeDir string) (string, error) {
	if p == "~" {
		if homeDir == "" {
			return "", &UserError{Msg: "cannot resolve ~: home directory is unknown", Hint: "Pass an absolute path.", Code: "bad_value", Kind: KindUsage}
		}
		return homeDir, nil
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if homeDir == "" {
			return "", &UserError{Msg: "cannot resolve ~: home directory is unknown", Hint: "Pass an absolute path.", Code: "bad_value", Kind: KindUsage}
		}
		p = filepath.Join(homeDir, filepath.FromSlash(rest))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", p, err)
	}
	return abs, nil
}

func defaultGuestCopyPath(localAbs, homeDir string) string {
	if homeDir != "" {
		if rel, err := filepath.Rel(homeDir, localAbs); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return path.Join("/home/agent", filepath.ToSlash(rel))
		}
	}
	return path.Join("/mnt", filepath.Base(localAbs))
}

func normalizeGuestCopyPath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) || strings.ContainsFunc(raw, unicode.IsControl) {
		return "", &UserError{
			Msg:  fmt.Sprintf("invalid guest path %q", CleanLine(raw)),
			Hint: "Use an absolute guest path or start it with ~/ (under /home/agent).",
			Code: "bad_value",
			Kind: KindUsage,
		}
	}
	if raw == "~" {
		return "/home/agent", nil
	}
	if rest, ok := strings.CutPrefix(raw, "~/"); ok {
		raw = "/home/agent/" + rest
	}
	if !strings.HasPrefix(raw, "/") {
		return "", &UserError{
			Msg:  fmt.Sprintf("guest path %q must be absolute or start with ~/", CleanLine(raw)),
			Hint: "Pass /home/agent/…, /mnt/…, /work/… or ~/….",
			Code: "bad_value",
			Kind: KindUsage,
		}
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", &UserError{
				Msg:  fmt.Sprintf("guest path %q must not contain ..", CleanLine(raw)),
				Hint: "Pass a clean absolute guest path.",
				Code: "bad_value",
				Kind: KindUsage,
			}
		}
	}
	clean := path.Clean(raw)
	if clean == "/" {
		return "", &UserError{
			Msg:  "guest path cannot be /",
			Hint: "Name a file or folder inside /home/agent, /work or /mnt.",
			Code: "bad_value",
			Kind: KindUsage,
		}
	}
	return clean, nil
}

func copyTooManyEntriesError(maxEntries int) *UserError {
	return &UserError{
		Msg:  fmt.Sprintf("laptop copies hold more than %d files", maxEntries),
		Hint: "Copy fewer files or folders.",
		Code: "cp_too_big",
		Kind: KindRefused,
	}
}

func itemEntryCount(entries []localEntry, isDir bool) int {
	if len(entries) == 0 && isDir {
		return 1
	}
	return len(entries)
}

// ValidateCopyIn checks every item against the paste guard's private-file
// rules (copy_sensitive) and enforces aggregate byte and entry caps using
// os.Root before connecting to the host (spec 17.3).
func ValidateCopyIn(items []CopyInItem, maxBytes int64, maxEntries int) error {
	if maxBytes <= 0 {
		maxBytes = DefaultCopyInMaxBytes
	}
	if maxEntries <= 0 {
		maxEntries = DefaultCopyInMaxEntries
	}
	var totalBytes int64
	totalEntries := 0
	for _, item := range items {
		root, abs, topName, st, err := openLocalSource(item.LocalPath)
		if err != nil {
			return err
		}
		entries, _, err := collectLocalEntries(root, filepath.Dir(abs), topName, item.LocalPath, st, CopyOptions{
			MaxBytes:   maxBytes,
			MaxEntries: maxEntries,
		})
		_ = root.Close()
		if err != nil {
			return err
		}
		totalEntries += itemEntryCount(entries, st.IsDir())
		if totalEntries > maxEntries {
			return copyTooManyEntriesError(maxEntries)
		}
		for _, e := range entries {
			totalBytes += e.size
		}
		if totalBytes > maxBytes {
			return copyTooBigError(totalBytes, maxBytes)
		}
	}
	return nil
}

// UploadCopyIn uploads each item into the session's staging SFTP directory
// using P1.6's os.Root + pkg/sftp upload engine while enforcing aggregate byte
// and entry caps across all items.
func UploadCopyIn(sc *sftp.Client, items []CopyInItem, maxBytes int64, maxEntries int) ([]api.CopyInFile, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultCopyInMaxBytes
	}
	if maxEntries <= 0 {
		maxEntries = DefaultCopyInMaxEntries
	}
	var totalBytes int64
	totalEntries := 0
	var out []api.CopyInFile
	for i, item := range items {
		root, abs, topName, st, err := openLocalSource(item.LocalPath)
		if err != nil {
			return nil, err
		}
		entries, _, err := collectLocalEntries(root, filepath.Dir(abs), topName, item.LocalPath, st, CopyOptions{
			MaxBytes:   maxBytes,
			MaxEntries: maxEntries,
		})
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		nEntries := itemEntryCount(entries, st.IsDir())
		if totalEntries+nEntries > maxEntries {
			_ = root.Close()
			return nil, copyTooManyEntriesError(maxEntries)
		}
		var plannedBytes int64
		for _, e := range entries {
			plannedBytes += e.size
		}
		if totalBytes+plannedBytes > maxBytes {
			_ = root.Close()
			return nil, copyTooBigError(totalBytes+plannedBytes, maxBytes)
		}
		stageRel := path.Join("copy", strconv.Itoa(i), filepath.Base(item.LocalPath))
		if err := sc.MkdirAll(path.Dir(stageRel)); err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("create staging folder for %s: %w", item.GuestPath, err)
		}
		dstBase, err := resolveRemotePath(sc, stageRel)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		remainingBytes := maxBytes - totalBytes
		_, copiedBytes, err := uploadLocalEntries(sc, root, dstBase, st.IsDir(), entries, CopyOptions{
			Force:      true,
			MaxBytes:   remainingBytes,
			MaxEntries: maxEntries - totalEntries,
		})
		_ = root.Close()
		if err != nil {
			var ue *UserError
			if errors.As(err, &ue) && ue.Code == "cp_too_big" {
				return nil, copyTooBigError(totalBytes+copiedBytes, maxBytes)
			}
			return nil, err
		}
		totalBytes += copiedBytes
		totalEntries += nEntries
		if totalBytes > maxBytes {
			return nil, copyTooBigError(totalBytes, maxBytes)
		}
		out = append(out, api.CopyInFile{
			StageRel:  stageRel,
			GuestPath: item.GuestPath,
			IsDir:     st.IsDir(),
		})
	}
	return out, nil
}

// ToAPICopyInSpecs converts CopyInItems to host manifest metadata so the host
// knows whether a restored session has pending laptop copies without trusting
// host-returned paths for re-upload (spec 17.3, G10).
func ToAPICopyInSpecs(items []CopyInItem) []api.CopyInSpec {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.CopyInSpec, len(items))
	for i, it := range items {
		out[i] = api.CopyInSpec{Source: it.Source, Guest: it.GuestPath}
	}
	return out
}
