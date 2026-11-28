package chv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

// RuntimeBundle holds the resolved VMM, ch-remote, guest kernel and verified
// dm-verity EROFS tools image needed to boot a Cloud Hypervisor microvm.
type RuntimeBundle struct {
	Label    string
	VMM      string
	CHRemote string
	Kernel   string
	Tools    DiskVerity
	Files    []string // SHA-256 hex digests referenced from artefacts/sha256 when loaded via H9 cache
}

// OpenedSandbox represents a running Cloud Hypervisor microvm opened by
// session ID via its VMM API socket (Driver.Open, P3.6). It supports graceful
// ACPI shutdown, socket cleanup and exit waiting without needing the original
// parent process handle.
type OpenedSandbox struct {
	id           string
	chRemotePath string
	apiSocket    string
	vsockSocket  string
}

// openSandbox implements Driver.Open(ctx, id).
func openSandbox(ctx context.Context, id string) (wall.Sandbox, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("chv open: missing session ID. Try: pass a valid session ID")
	}
	apiSock := paths.VMMSocket(id)
	vsockSock := paths.VsockSocket(id)
	if _, err := os.Stat(apiSock); err != nil {
		return nil, fmt.Errorf("microvm %s is not running (no VMM socket at %s). Try: wake it with attach or resume", id, apiSock)
	}
	rp := DefaultRuntimePaths()
	if b, err := ResolveRuntimeBundle(""); err == nil && b.CHRemote != "" {
		rp.CHRemote = b.CHRemote
	}
	if _, err := Info(ctx, rp.CHRemote, apiSock); err != nil {
		return nil, fmt.Errorf("microvm %s did not answer ch-remote info: %w. Try: stop or resume the session", id, err)
	}
	return &OpenedSandbox{
		id:           id,
		chRemotePath: rp.CHRemote,
		apiSocket:    apiSock,
		vsockSocket:  vsockSock,
	}, nil
}

// Start is not valid on an already-running microvm handle.
func (o *OpenedSandbox) Start(context.Context) (net.Conn, error) {
	return nil, errors.New("microvm sandbox is already running. Try: stop the session before starting it again")
}

// Stop sends an ACPI power-button event via ch-remote and waits up to grace
// for the VMM API socket to stop answering.
func (o *OpenedSandbox) Stop(ctx context.Context, grace time.Duration) error {
	if grace <= 0 {
		grace = defaultStopGrace
	}
	_ = PowerButton(ctx, o.chRemotePath, o.apiSocket)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !o.running(ctx) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return o.killCtx(ctx)
}

// Kill asks Cloud Hypervisor to shut down immediately via ch-remote shutdown.
func (o *OpenedSandbox) Kill() error {
	return o.killCtx(context.Background())
}

func (o *OpenedSandbox) killCtx(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	_ = Shutdown(ctx, o.chRemotePath, o.apiSocket)
	return nil
}

// Wait polls until the opened microvm's API socket stops answering.
func (o *OpenedSandbox) Wait() wall.Exit {
	ctx := context.Background()
	for o.running(ctx) {
		time.Sleep(100 * time.Millisecond)
	}
	return wall.Exit{}
}

// Destroy removes the microvm's temporary VMM and vsock sockets while leaving
// the session disk intact for resume or explicit deletion.
func (o *OpenedSandbox) Destroy() error {
	var firstErr error
	for _, p := range []string{o.apiSocket, o.vsockSocket} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return firstErr
}

// Abilities reports optional capabilities of an opened microvm handle.
func (o *OpenedSandbox) Abilities() wall.Abilities { return wall.Abilities{} }

func (o *OpenedSandbox) running(ctx context.Context) bool {
	if _, err := os.Stat(o.apiSocket); err != nil {
		return false
	}
	tctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := Info(tctx, o.chRemotePath, o.apiSocket)
	return err == nil
}

// VerifySessionDiskCap checks that the session disk at path has apparent size
// and host allocated blocks (matching `du`) at or below capBytes (spec 17.1).
func VerifySessionDiskCap(path string, capBytes int64) (allocated, apparent int64, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, fmt.Errorf("stat session disk %s: %w", path, err)
	}
	apparent = fi.Size()
	allocated, err = SessionDiskAllocatedBytes(path)
	if err != nil {
		return 0, apparent, err
	}
	if capBytes > 0 && apparent > capBytes {
		return allocated, apparent, fmt.Errorf("session disk apparent size %d exceeds disk_max %d", apparent, capBytes)
	}
	if capBytes > 0 && allocated > capBytes {
		return allocated, apparent, fmt.Errorf("session disk allocated size %d exceeds disk_max %d", allocated, capBytes)
	}
	return allocated, apparent, nil
}

// ResolveRuntimeBundle locates the Cloud Hypervisor VMM, ch-remote, guest
// kernel and verified dm-verity EROFS tools image on this host. It checks the
// default pinned runtime folder first (artefacts/runtime/<RuntimeLabel>/),
// then falls back to H9's stored runtime label manifests under
// artefacts/runtime/<label>.json pointing into artefacts/sha256/.
func ResolveRuntimeBundle(preferredLabel string) (RuntimeBundle, error) {
	if b, ok := resolveDefaultRuntimeDir(preferredLabel); ok {
		return b, nil
	}
	if b, ok := resolveStoredRuntimeLabel(preferredLabel); ok {
		return b, nil
	}
	rp := DefaultRuntimePaths()
	return RuntimeBundle{}, fmt.Errorf(
		"microvm runtime files (cloud-hypervisor, ch-remote, vmlinux, tools.erofs) are not installed in %s. Try: run host prepare or install the pinned %s runtime",
		rp.Dir, RuntimeLabel,
	)
}

func resolveDefaultRuntimeDir(preferredLabel string) (RuntimeBundle, bool) {
	label := preferredLabel
	if label == "" {
		label = RuntimeLabel
	}
	rp := RuntimePathsIn(filepath.Join(paths.RuntimeArtefactsDir(), label))
	for _, p := range []string{rp.VMM, rp.CHRemote, rp.Kernel} {
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			return RuntimeBundle{}, false
		}
	}
	tools, ok := readToolsSidecar(filepath.Join(rp.Dir, "tools.erofs"))
	if !ok {
		return RuntimeBundle{}, false
	}
	return RuntimeBundle{
		Label:    label,
		VMM:      rp.VMM,
		CHRemote: rp.CHRemote,
		Kernel:   rp.Kernel,
		Tools:    tools,
	}, true
}

func readToolsSidecar(toolsPath string) (DiskVerity, bool) {
	if fi, err := os.Stat(toolsPath); err != nil || fi.IsDir() {
		return DiskVerity{}, false
	}
	b, err := os.ReadFile(toolsPath + ".json")
	if err != nil {
		return DiskVerity{}, false
	}
	var rec ToolsRecord
	if err := json.Unmarshal(b, &rec); err != nil || rec.RootHash == "" || rec.DataBlocks == 0 {
		return DiskVerity{}, false
	}
	return DiskVerity{
		Path:          toolsPath,
		RootHash:      rec.RootHash,
		DataBlocks:    rec.DataBlocks,
		HasSuperblock: false,
	}, true
}

func resolveStoredRuntimeLabel(preferredLabel string) (RuntimeBundle, bool) {
	var candidates []string
	if preferredLabel != "" {
		candidates = append(candidates, paths.RuntimeLabelFile(preferredLabel))
	}
	if entries, err := os.ReadDir(paths.ArtefactsRuntimeDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(paths.ArtefactsRuntimeDir(), e.Name())
			if preferredLabel == "" || p != paths.RuntimeLabelFile(preferredLabel) {
				candidates = append(candidates, p)
			}
		}
	}
	arch := runtime.GOARCH
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rec api.StoredRuntimeLabel
		if json.Unmarshal(b, &rec) != nil {
			continue
		}
		if bundle, ok := bundleFromStoredLabel(rec, arch); ok {
			return bundle, true
		}
	}
	return RuntimeBundle{}, false
}

func bundleFromStoredLabel(rec api.StoredRuntimeLabel, arch string) (RuntimeBundle, bool) {
	b := RuntimeBundle{Label: rec.Runtime}
	var toolsPath string
	for _, it := range rec.Items {
		if it.Arch != "" && it.Arch != arch {
			continue
		}
		hexDigest, ok := api.NormalizeSHA256Hex(it.SHA256)
		if !ok {
			continue
		}
		artPath := paths.ArtefactFile(hexDigest)
		if fi, err := os.Stat(artPath); err != nil || fi.IsDir() {
			continue
		}
		switch {
		case it.Kind == "vmm" || strings.HasPrefix(it.Name, "cloud-hypervisor"):
			b.VMM = artPath
			b.Files = append(b.Files, hexDigest)
		case it.Kind == "ch-remote" || strings.HasPrefix(it.Name, "ch-remote"):
			b.CHRemote = artPath
			b.Files = append(b.Files, hexDigest)
		case it.Kind == "kernel" || strings.HasPrefix(it.Name, "vmlinux"):
			b.Kernel = artPath
			b.Files = append(b.Files, hexDigest)
		case it.Kind == "tools" || strings.HasPrefix(it.Name, "tools"):
			toolsPath = artPath
			b.Files = append(b.Files, hexDigest)
		}
	}
	if b.VMM == "" || b.CHRemote == "" || b.Kernel == "" || toolsPath == "" {
		return RuntimeBundle{}, false
	}
	tools, ok := readToolsSidecar(toolsPath)
	if !ok {
		return RuntimeBundle{}, false
	}
	b.Tools = tools
	return b, true
}
