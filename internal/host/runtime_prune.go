package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// keepRuntimeLabels is how many of the newest runtime labels the host cache
// always retains (isolation spec 14.9.4).
const keepRuntimeLabels = 2

type storedLabelEntry struct {
	api.StoredRuntimeLabel
	path    string
	modTime time.Time
}

// readStoredRuntimeLabels returns every valid artefacts/runtime/<label>.json
// sorted newest first (by RecordedAt, then file ModTime, then label descending).
func readStoredRuntimeLabels() []storedLabelEntry {
	dir := paths.ArtefactsRuntimeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil
	}
	defer func() { _ = root.Close() }()

	var out []storedLabelEntry
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		label, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !api.ValidRuntimeLabel(label) {
			continue
		}
		fi, err := root.Lstat(e.Name())
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		raw, err := root.ReadFile(e.Name())
		if err != nil {
			continue
		}
		var rec api.StoredRuntimeLabel
		if json.Unmarshal(raw, &rec) != nil {
			continue
		}
		if rec.Runtime == "" {
			rec.Runtime = label
		}
		if rec.RecordedAt.IsZero() {
			rec.RecordedAt = fi.ModTime().UTC()
		}
		out = append(out, storedLabelEntry{
			StoredRuntimeLabel: rec,
			path:               filepath.Join(dir, e.Name()),
			modTime:            fi.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].RecordedAt.After(out[j].RecordedAt)
		}
		if !out[i].modTime.Equal(out[j].modTime) {
			return out[i].modTime.After(out[j].modTime)
		}
		return out[i].Runtime > out[j].Runtime
	})
	return out
}

func recordRuntimeLabel(label, release string, installed, labelItems []api.RuntimeAsset) error {
	items := installed
	if len(labelItems) > 0 {
		items = make([]api.RuntimeAsset, 0, len(labelItems))
		for _, raw := range labelItems {
			it, err := validateRuntimeAsset(raw, false)
			if err != nil {
				return err
			}
			fi, err := os.Lstat(paths.ArtefactFile(it.SHA256))
			if err != nil || !fi.Mode().IsRegular() || fi.Size() != it.Size {
				return UserError("runtime_missing", fmt.Sprintf("runtime file %s (%s) is not present in the host cache", it.Name, it.SHA256[:12]), "Retry installing the runtime.")
			}
			items = append(items, it)
		}
	}
	return writeStoredRuntimeLabel(label, release, items)
}

func writeStoredRuntimeLabel(label, release string, items []api.RuntimeAsset) error {
	rec := api.StoredRuntimeLabel{
		Runtime:    label,
		Release:    release,
		RecordedAt: time.Now().UTC(),
		Items:      items,
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime label %s: %w", label, err)
	}
	raw = append(raw, '\n')
	dir := paths.ArtefactsRuntimeDir()
	tmp := filepath.Join(dir, "."+label+"."+randomHex(6)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write runtime label %s: %w", label, err)
	}
	if err := os.Rename(tmp, paths.RuntimeLabelFile(label)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install runtime label %s: %w", label, err)
	}
	return nil
}

// PruneRuntime removes unreferenced runtime artefacts, old runtime labels and
// stale incoming part files according to isolation spec 14.9.4:
//   - Keep every file named by one of the two newest runtime labels.
//   - Keep every file (or runtime label) named by a non-deleted session manifest.
//   - Keep every file that arrived within the last hour (pruneGrace).
func PruneRuntime() ([]string, error) {
	old := oldRuntimeFiles()
	if len(old) == 0 {
		return nil, nil
	}
	names := make([]string, len(old))
	for i, p := range old {
		names[i] = filepath.Base(p)
	}
	return names, removeAll(old, os.Remove)
}

func oldRuntimeFiles() []string {
	labels := readStoredRuntimeLabels()
	keepDigests, keepLabels := sessionRuntimeRefs()

	var old []string
	for i, l := range labels {
		if i < keepRuntimeLabels || keepLabels[l.Runtime] || time.Since(l.modTime) < pruneGrace {
			keepLabels[l.Runtime] = true
			markLabelDigests(keepDigests, l.Items)
			continue
		}
		old = append(old, l.path)
	}
	old = append(old, oldArtefactFiles(keepDigests)...)
	old = append(old, oldIncomingPartFiles()...)
	sort.Strings(old)
	return old
}

func markLabelDigests(keep map[string]bool, items []api.RuntimeAsset) {
	for _, it := range items {
		if hexDigest, ok := api.NormalizeSHA256Hex(it.SHA256); ok {
			keep[hexDigest] = true
		}
	}
}

func oldArtefactFiles(keepDigests map[string]bool) []string {
	entries, err := os.ReadDir(paths.ArtefactsSHA256Dir())
	if err != nil {
		return nil
	}
	var old []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(paths.ArtefactsSHA256Dir(), e.Name())
		fi, err := os.Lstat(p)
		if err != nil || time.Since(fi.ModTime()) < pruneGrace {
			continue
		}
		if hexDigest, ok := api.NormalizeSHA256Hex(e.Name()); ok && keepDigests[hexDigest] {
			continue
		}
		old = append(old, p)
	}
	return old
}

func oldIncomingPartFiles() []string {
	entries, err := os.ReadDir(paths.ArtefactsIncomingDir())
	if err != nil {
		return nil
	}
	var old []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		p := filepath.Join(paths.ArtefactsIncomingDir(), e.Name())
		fi, err := os.Lstat(p)
		if err != nil || time.Since(fi.ModTime()) < pruneGrace {
			continue
		}
		old = append(old, p)
	}
	return old
}

func sessionRuntimeRefs() (digests, labels map[string]bool) {
	digests = map[string]bool{}
	labels = map[string]bool{}
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return digests, labels
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := LoadManifest(e.Name())
		if err != nil || m.Wall == nil {
			continue
		}
		if m.Wall.Runtime != "" {
			labels[m.Wall.Runtime] = true
		}
		for _, d := range m.Wall.RuntimeFiles {
			if hexDigest, ok := api.NormalizeSHA256Hex(d); ok {
				digests[hexDigest] = true
			}
		}
	}
	return digests, labels
}

func oldRuntimeFix() []fixItem {
	old := oldRuntimeFiles()
	if len(old) == 0 {
		return nil
	}
	var size int64
	for _, p := range old {
		if fi, err := os.Lstat(p); err == nil {
			size += fi.Size()
		}
	}
	return []fixItem{{Fix: api.Fix{
		ID:      "old-runtime",
		Problem: fmt.Sprintf("%s that nothing uses take %.1f MB", plural(len(old), "old runtime file", "old runtime files"), float64(size)/1e6),
		Action:  fmt.Sprintf("remove them, and keep the newest %d runtime labels, any runtime used by a session, and files from the last hour", keepRuntimeLabels),
		Command: "rm " + strings.Join(old, " "),
	}, run: func() error { return removeAll(old, os.Remove) }}}
}
