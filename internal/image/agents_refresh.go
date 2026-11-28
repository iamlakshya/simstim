package image

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"

	"github.com/iamlakshya/simstim/internal/api"
)

// AgentsDiskFormat names the filesystem image format of a built agents disk.
const (
	AgentsFormatEROFS = "erofs"
	AgentsFormatExt4  = "ext4"
)

// EnsureAgentsDisk returns the newest read-only agents disk for sources,
// reusing latest.json without rebuilding when its source digest and format
// still match and the image file exists on disk. When any installed agent was
// added, updated, replaced (even with identical size and mtime) or removed, it
// builds and records a new disk (spec 20.4).
func (s *Store) EnsureAgentsDisk(ctx context.Context, sources []AgentSource, opts AgentsDiskOptions) (rec api.AgentsDiskRecord, rebuilt bool, err error) {
	maxBytes, maxEntries := agentsDiskCaps(opts)
	plan, agentKinds, err := collectAgentsPlan(sources, maxBytes, maxEntries)
	if err != nil {
		return api.AgentsDiskRecord{}, false, err
	}
	defer plan.close()

	_, desiredFormat, err := resolveAgentsBuilder(opts)
	if err != nil {
		return api.AgentsDiskRecord{}, false, err
	}
	srcDigest := plan.sourceDigest()
	if latest, ok := s.reusableAgentsDisk(srcDigest, desiredFormat); ok {
		return latest, false, nil
	}
	return s.buildAgentsDiskFromPlan(ctx, plan, agentKinds, srcDigest, opts, true)
}

func (s *Store) reusableAgentsDisk(srcDigest, desiredFormat string) (api.AgentsDiskRecord, bool) {
	latest, err := s.LatestAgentsDisk()
	if err != nil || latest.SourceDigest != srcDigest || latest.Path == "" {
		return api.AgentsDiskRecord{}, false
	}
	gotFormat := latest.Format
	if gotFormat == "" {
		gotFormat = AgentsFormatEROFS
	}
	if desiredFormat != "" && gotFormat != desiredFormat {
		return api.AgentsDiskRecord{}, false
	}
	fi, serr := os.Stat(latest.Path)
	if serr != nil || !fi.Mode().IsRegular() {
		return api.AgentsDiskRecord{}, false
	}
	return latest, true
}

// ResolveAgentsDisk validates digest ("sha256:<hex>" or "<hex>") and returns
// the read-only disk image path stored under <store>/agents/.
func (s *Store) ResolveAgentsDisk(digest string) (string, error) {
	hexDigest := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(digest), "sha256:"))
	if !isHex64(hexDigest) {
		return "", agentsDiskError(fmt.Sprintf("invalid agents disk digest %q", digest), "Rebuild the agents disk on the host.", nil)
	}
	root, err := os.OpenRoot(s.AgentsDir())
	if err != nil {
		return "", agentsDiskError("the host has no agents disk store yet", "Run `agents install` or rebuild the agents disk on the host.", err)
	}
	defer root.Close()
	for _, ext := range []string{".erofs", ".img"} {
		name := hexDigest + ext
		if fi, serr := root.Stat(name); serr == nil && fi.Mode().IsRegular() {
			return filepath.Join(s.AgentsDir(), name), nil
		}
	}
	return "", agentsDiskError(fmt.Sprintf("agents disk sha256:%s is missing from the store", hexDigest), "Rebuild the agents disk on the host.", os.ErrNotExist)
}

func agentsDiskCaps(opts AgentsDiskOptions) (maxBytes int64, maxEntries int) {
	maxBytes = opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxAgentsDiskBytes
	}
	maxEntries = opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = MaxAgentsDiskEntries
	}
	return maxBytes, maxEntries
}

// resolveAgentsBuilder picks mkfs.erofs when available (preferred per reuse.md)
// or pure-Go hcsshim tar2ext4 when opts.AllowExt4Fallback is enabled (spec 20.4).
func resolveAgentsBuilder(opts AgentsDiskOptions) (mkfsBin, format string, err error) {
	if opts.MkfsErofs != "" {
		return opts.MkfsErofs, AgentsFormatEROFS, nil
	}
	if p, ok := findMkfsErofs(); ok {
		return p, AgentsFormatEROFS, nil
	}
	if opts.AllowExt4Fallback {
		return "", AgentsFormatExt4, nil
	}
	return "", "", agentsDiskError(
		"mkfs.erofs is not installed on this host",
		"Install erofs-utils (for example, `sudo apt-get install erofs-utils`).",
		exec.ErrNotFound,
	)
}

func findMkfsErofs() (string, bool) {
	if p, err := exec.LookPath("mkfs.erofs"); err == nil {
		return p, true
	}
	// On Debian and Ubuntu, mkfs.erofs lives in /usr/sbin which non-root PATH
	// often omits; check standard sbin locations only when PATH is non-empty
	// and not an isolated test override without standard system directories.
	pathEnv := os.Getenv("PATH")
	if !strings.Contains(pathEnv, "/usr/bin") && !strings.Contains(pathEnv, "/bin") {
		return "", false
	}
	for _, p := range []string{"/usr/sbin/mkfs.erofs", "/sbin/mkfs.erofs"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// runTar2Ext4 builds a deterministic read-only ext4 agents disk (without
// dm-verity) using hcsshim's tar2ext4.Convert when mkfs.erofs is absent.
func runTar2Ext4(ctx context.Context, outPath string, plan *agentsPlan, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("build ext4 agents disk: %w", err)
	}
	f, err := os.OpenFile(outPath, os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open temp ext4 agents disk: %w", err)
	}
	pr, pw := io.Pipe()
	writeErrCh := make(chan error, 1)
	go func() {
		werr := writeAgentsTar(pw, plan)
		_ = pw.CloseWithError(werr)
		writeErrCh <- werr
	}()

	convErr := tar2ext4.Convert(pr, f, tar2ext4.MaximumDiskSize(maxBytes))
	_ = pr.Close()
	writeErr := <-writeErrCh
	syncErr := f.Sync()
	closeErr := f.Close()

	if writeErr != nil {
		return writeErr
	}
	if convErr != nil {
		return agentsDiskError(fmt.Sprintf("convert agents tar to ext4: %v", convErr), "", convErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync ext4 agents disk: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close ext4 agents disk: %w", closeErr)
	}
	return nil
}

func agentsDiskExt(format string) string {
	if format == AgentsFormatExt4 {
		return ".img"
	}
	return ".erofs"
}

func cutAgentsDiskName(name string) (hexName string, ok bool) {
	for _, ext := range []string{".erofs", ".img"} {
		if h, found := strings.CutSuffix(name, ext); found && isHex64(h) {
			return h, true
		}
	}
	return "", false
}
