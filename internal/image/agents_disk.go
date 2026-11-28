package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// Spec 20.4 caps for the read-only agents disk.
const (
	MaxAgentsDiskBytes   int64 = 4 << 30 // 4 GiB
	MaxAgentsDiskEntries       = 500_000 // 500,000 entries
	agentsUID                  = 1000
	agentsGID                  = 1000
)

// AgentSource describes one installed agent on the host to copy into the
// read-only agents disk.
type AgentSource struct {
	Kind        string
	BinaryName  string
	BinaryPath  string
	InstallDirs []string
}

// AgentsDiskOptions configures BuildAgentsDisk. Zero fields use defaults.
type AgentsDiskOptions struct {
	MkfsErofs  string // path to mkfs.erofs; defaults to exec.LookPath("mkfs.erofs")
	MaxBytes   int64
	MaxEntries int
}

// AgentsDir returns the agents-disk directory inside the store.
func (s *Store) AgentsDir() string { return filepath.Join(s.Dir, "agents") }

// BuildAgentsDisk streams the host's installed agents as one deterministic tar
// archive directly into `mkfs.erofs --tar=f --force-uid=1000 --force-gid=1000`
// and stores the resulting read-only EROFS image at
// `<store>/agents/<sha256>.erofs` (reuse.md part A, spec 20.4).
func (s *Store) BuildAgentsDisk(ctx context.Context, sources []AgentSource, opts AgentsDiskOptions) (api.AgentsDiskRecord, error) {
	mkfsBin := opts.MkfsErofs
	if mkfsBin == "" {
		var err error
		mkfsBin, err = exec.LookPath("mkfs.erofs")
		if err != nil {
			return api.AgentsDiskRecord{}, agentsDiskError(
				"mkfs.erofs is not installed on this host",
				"Install erofs-utils (for example, `sudo apt-get install erofs-utils`).",
				err,
			)
		}
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxAgentsDiskBytes
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = MaxAgentsDiskEntries
	}

	plan, agentKinds, err := collectAgentsPlan(sources, maxBytes, maxEntries)
	if err != nil {
		return api.AgentsDiskRecord{}, err
	}
	defer plan.close()

	agentsDir := s.AgentsDir()
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		return api.AgentsDiskRecord{}, fmt.Errorf("create agents disk folder: %w", err)
	}
	unlock, err := lockFile(filepath.Join(agentsDir, "build.lock"), true)
	if err != nil {
		return api.AgentsDiskRecord{}, err
	}
	defer unlock()

	tmpFile, err := os.CreateTemp(agentsDir, ".agents-*.erofs")
	if err != nil {
		return api.AgentsDiskRecord{}, fmt.Errorf("create temporary agents disk: %w", err)
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := runMkfsErofs(ctx, mkfsBin, tmpPath, plan); err != nil {
		return api.AgentsDiskRecord{}, err
	}

	hexDigest, size, err := hashFile(tmpPath)
	if err != nil {
		return api.AgentsDiskRecord{}, fmt.Errorf("hash agents disk: %w", err)
	}
	if size > maxBytes {
		return api.AgentsDiskRecord{}, agentsDiskError(
			fmt.Sprintf("agents disk is %d bytes, over the %d byte limit", size, maxBytes),
			"", nil,
		)
	}
	//nolint:gosec // G302: read-only disk image, mode 0444 per spec 20.4
	if err := os.Chmod(tmpPath, 0o444); err != nil {
		return api.AgentsDiskRecord{}, fmt.Errorf("set agents disk mode: %w", err)
	}

	finalPath := filepath.Join(agentsDir, hexDigest+".erofs")
	if _, err := os.Stat(finalPath); err == nil {
		_ = os.Remove(tmpPath)
		cleanup = false
	} else {
		if err := os.Rename(tmpPath, finalPath); err != nil {
			return api.AgentsDiskRecord{}, fmt.Errorf("install agents disk: %w", err)
		}
		cleanup = false
	}

	rec := api.AgentsDiskRecord{
		Digest:  "sha256:" + hexDigest,
		Path:    finalPath,
		Size:    size,
		Agents:  agentKinds,
		BuiltAt: s.now(),
	}
	if err := writeJSONAtomic(filepath.Join(agentsDir, "latest.json"), rec); err != nil {
		return api.AgentsDiskRecord{}, fmt.Errorf("save latest agents disk record: %w", err)
	}
	return rec, nil
}

// LatestAgentsDisk returns the newest built agents disk record, if present.
func (s *Store) LatestAgentsDisk() (api.AgentsDiskRecord, error) {
	b, err := os.ReadFile(filepath.Join(s.AgentsDir(), "latest.json"))
	if err != nil {
		return api.AgentsDiskRecord{}, fmt.Errorf("read latest agents disk: %w", err)
	}
	var rec api.AgentsDiskRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return api.AgentsDiskRecord{}, fmt.Errorf("decode latest agents disk: %w", err)
	}
	return rec, nil
}

// PruneAgentsDisks removes old agents disks that are neither the latest disk
// nor listed in keepHexes (spec 20.4).
func (s *Store) PruneAgentsDisks(keepHexes map[string]bool) ([]string, error) {
	agentsDir := s.AgentsDir()
	entries, err := os.ReadDir(agentsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agents disks: %w", err)
	}
	latestHex := ""
	if rec, err := s.LatestAgentsDisk(); err == nil {
		latestHex = strings.TrimPrefix(rec.Digest, "sha256:")
	}
	var removed []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		hexName, ok := strings.CutSuffix(e.Name(), ".erofs")
		if !ok || !isHex64(hexName) {
			continue
		}
		if hexName == latestHex || keepHexes[hexName] {
			continue
		}
		if err := os.Remove(filepath.Join(agentsDir, e.Name())); err == nil {
			removed = append(removed, "sha256:"+hexName)
		}
	}
	sort.Strings(removed)
	return removed, nil
}

func runMkfsErofs(ctx context.Context, mkfsBin, outPath string, plan *agentsPlan) error {
	args := []string{
		"--tar=f",
		fmt.Sprintf("--force-uid=%d", agentsUID),
		fmt.Sprintf("--force-gid=%d", agentsGID),
		"-U", "clear",
		"-T", "0",
		"--mkfs-time",
		"--preserve-mtime",
		"--quiet",
		outPath,
	}
	cmd := exec.CommandContext(ctx, mkfsBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open mkfs.erofs pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return agentsDiskError(fmt.Sprintf("start mkfs.erofs: %v", err), "", err)
	}

	writeErr := writeAgentsTar(stdin, plan)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("close mkfs.erofs stdin: %w", closeErr)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return agentsDiskError("mkfs.erofs failed: "+msg, "", waitErr)
	}
	return nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", p, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash %s: %w", p, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func writeJSONAtomic(dst string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp json: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp json: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp json: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp json: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename json: %w", err)
	}
	return nil
}
