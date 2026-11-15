//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/chv"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// NewCloudHypervisorDriver joins the complete driver to canonical host facts
// and trusted session manifests. The registered factory uses it for both
// capability reporting and the supervisor's fresh expected-plan controller.
func NewCloudHypervisorDriver() (*chv.Driver, error) {
	return chv.New(chv.Options{Facts: microVMFacts, Identity: microVMIdentity, Domain: microVMStoreDomain, Reference: recordVMAuthority, Diagnostic: recordVMDiagnostic})
}

func microVMStoreDomain(ctx context.Context) (cachefs.Domain, error) {
	if err := CheckStartContext(ctx); err != nil {
		return cachefs.Domain{}, err
	}
	owner, err := placementForCall()
	if err != nil {
		return cachefs.Domain{}, err
	}
	return owner.domain(), nil
}

var microVMHelperIdentity = sync.OnceValue(func() string { return executableWallVersion("/proc/self/exe") })

func microVMIdentity() (string, error) {
	boot, helper := BootID(), microVMHelperIdentity()
	if boot == "" || helper == "" {
		return "", fmt.Errorf("the host boot or running helper byte identity is unavailable")
	}
	return boot + "/" + helper, nil
}

func microVMFacts(ctx context.Context) (api.CapProbe, error) {
	source := liveCapSource()
	source.wallProbes = func(ctx context.Context) []wall.Probe { return []wall.Probe{process.Probe(ctx)} }
	return capabilityProbe(ctx, "", source)
}

// The driver calls this inside the admitted supervisor, not through a bridge
// operation. The image lease remains held until the canonical manifest Save.
func recordVMAuthority(ctx context.Context, spec wall.Spec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if spec.VM == nil || !validID(spec.ID) || spec.SessionDir != paths.SessionDir(spec.ID) {
		return fmt.Errorf("trusted microvm reference names another session")
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionsDir(), false)
	if err != nil {
		return err
	}
	defer root.Close()
	manifest, err := readRuntimeReference(root, spec.ID)
	if err != nil {
		return err
	}
	if err := SetVMReferences(&manifest, spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return manifest.Save()
}

func recordVMDiagnostic(ctx context.Context, spec wall.Spec, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(spec.ID) || spec.SessionDir != paths.SessionDir(spec.ID) || len(data) > 64<<10 {
		return fmt.Errorf("VMM diagnostics exceed their trusted session boundary")
	}
	if len(data) == 0 {
		return nil
	}
	root, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := cachefs.OpenFile(root, filepath.Base(paths.SupervisorLog(spec.ID)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return fmt.Errorf("append bounded VMM diagnostics: %w", err)
	}
	return nil
}
