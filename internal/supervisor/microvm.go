package supervisor

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

// chvSandbox adapts *chv.Sandbox (which returns wall.Exit) to the supervisor's
// Sandbox interface and exposes Cloud Hypervisor's captured stderr tail.
type chvSandbox struct {
	inner *chv.Sandbox
}

func (c *chvSandbox) Start(ctx context.Context) (net.Conn, error) { return c.inner.Start(ctx) }
func (c *chvSandbox) Stop(ctx context.Context, grace time.Duration) error {
	return c.inner.Stop(ctx, grace)
}
func (c *chvSandbox) Kill() error    { return c.inner.Kill() }
func (c *chvSandbox) Destroy() error { return c.inner.Destroy() }
func (c *chvSandbox) Stderr() string { return c.inner.Stderr() }
func (c *chvSandbox) Wait() Exit {
	ex := c.inner.Wait()
	return Exit{Code: ex.Code, Signal: ex.Signal, Err: ex.Err}
}

// runMicroVM resolves the microvm runtime, rootfs image and optional agents
// disk recorded on m and starts the supervisor over Cloud Hypervisor.
func runMicroVM(id, exe string, m *host.Manifest) error {
	sb, err := NewMicroVMSandbox(id, m)
	if err != nil {
		return err
	}
	return RunWith(id, sb, Options{EgressExe: exe})
}

// NewMicroVMSandbox builds a Cloud Hypervisor microvm Sandbox from m.
func NewMicroVMSandbox(id string, m *host.Manifest) (Sandbox, error) {
	label := ""
	if m.Wall != nil {
		label = m.Wall.Runtime
	}
	bundle, err := chv.ResolveRuntimeBundle(label)
	if err != nil {
		return nil, err
	}
	rootfs, err := resolveMicroVMRootfs(m)
	if err != nil {
		return nil, err
	}
	agentsPath := resolveMicroVMAgentsDisk(m)

	cpus := m.Limits.CPUs
	if cpus <= 0 {
		cpus = 2
	}
	mem := m.Limits.MemoryMax
	if mem <= 0 {
		mem = 1 << 30
	}
	diskMax := m.Limits.DiskMax
	if diskMax <= 0 {
		diskMax = chv.DefaultSessionDiskBytes
	}

	bwrapPath, _ := exec.LookPath("bwrap")

	initArgs := []string{"wall-init"}
	if m.Wall != nil {
		if m.Wall.System == "keep" {
			initArgs = append(initArgs, "--keep-system")
		}
		if m.Wall.Sudo {
			initArgs = append(initArgs, "--sudo")
		}
		if m.Wall.Docker {
			initArgs = append(initArgs, "--docker")
		}
	}

	return &chvSandbox{
		inner: chv.NewSandbox(chv.SandboxOptions{
			Config: chv.VMConfig{
				VMMPath:         bundle.VMM,
				CHRemotePath:    bundle.CHRemote,
				APISocket:       paths.VMMSocket(id),
				VsockSocket:     paths.VsockSocket(id),
				KernelPath:      bundle.Kernel,
				CPUs:            cpus,
				MemoryBytes:     mem,
				Tools:           bundle.Tools,
				Image:           rootfs,
				AgentsDiskPath:  agentsPath,
				SessionDiskPath: paths.SessionDisk(id),
				SessionDirect:   true,
				InitArgs:        initArgs,
			},
			SessionDiskBytes: diskMax,
			BwrapPath:        bwrapPath,
			BootTimeout:      chv.DefaultBootTimeout,
		}),
	}, nil
}

func resolveMicroVMRootfs(m *host.Manifest) (*chv.DiskVerity, error) {
	if m.Image == nil {
		return nil, fmt.Errorf("microvm session %s has no root image recorded in its manifest. Try: recreate the session after pulling an image", m.ID)
	}
	query := m.Image.PlatformDigest
	if query == "" {
		query = m.Image.Digest
	}
	rec, imgPath, err := host.ImageStore().Get(query)
	if err != nil {
		return nil, fmt.Errorf("locate microvm root image %s: %w", query, err)
	}
	f, err := os.Open(filepath.Clean(imgPath))
	if err != nil {
		return nil, fmt.Errorf("open microvm root image %s: %w", imgPath, err)
	}
	rootHash, dataBlocks, _, inspectErr := image.InspectExt4Verity(f)
	_ = f.Close()
	if inspectErr != nil {
		return nil, fmt.Errorf("inspect microvm root image %s: %w", imgPath, inspectErr)
	}
	wantHash := m.Image.RootHash
	if wantHash == "" {
		wantHash = rec.RootHash
	}
	if wantHash != "" && !strings.EqualFold(rootHash, wantHash) {
		return nil, fmt.Errorf("microvm root image %s root hash %s does not match recorded %s. Try: remove and re-pull the image", imgPath, rootHash, wantHash)
	}
	return &chv.DiskVerity{
		Path:          imgPath,
		RootHash:      rootHash,
		DataBlocks:    dataBlocks,
		HasSuperblock: true,
	}, nil
}

func resolveMicroVMAgentsDisk(m *host.Manifest) string {
	if m.Agents != "" {
		if p, err := host.AgentsDiskPath(m.Agents); err == nil {
			return p
		}
	}
	if rec, err := host.ImageStore().LatestAgentsDisk(); err == nil && rec.Path != "" {
		if fi, serr := os.Stat(rec.Path); serr == nil && fi.Mode().IsRegular() {
			return rec.Path
		}
	}
	return ""
}
