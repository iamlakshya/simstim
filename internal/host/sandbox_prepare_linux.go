//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

const sandboxPrepareHostPathV1 = "/usr/bin:/bin"
const sandboxPrepareScratchSlackV1 int64 = 64 << 20

type sandboxPrepareHookResultV1 struct {
	bootMS             int64
	bootErr            error
	stopUnconfirmedErr error
	stopConfirmed      <-chan struct{} // test-only settlement of that same injected owner
}

// Package-private hooks used only by host unit tests to exercise physical
// probe, disk headroom, timeout, client-loss and unconfirmed-exit branches
// without manufacturing public callback constructors.
var (
	sandboxPrepareProbeOverrideV1 func(ctx context.Context, b *bridge, witness *sandboxPrepareWitnessV1) error
	sandboxPrepareDiskFreeV1      = statfsFreeBytesV1
	sandboxPrepareBootOverrideV1  func(ctx context.Context, operation *SessionOperation, reservation *sessionReservation, m *Manifest) (*sandboxPrepareHookResultV1, bool)
)

func (b *bridge) runSandboxPreparePlatformV1(ctx context.Context, _ time.Time, witness *sandboxPrepareWitnessV1) (_ api.SandboxPrepareResultV1, returned error) {
	defaultHint := "Run " + brand.Name + " host doctor and check the host's sandbox setup before retrying."
	if err := probeSandboxPrepareDriverV1(ctx, b, witness); err != nil {
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, defaultHint)
	}
	if err := checkSandboxPrepareDiskV1(witness); err != nil {
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, "Free disk space on the host, then run "+brand.Name+" host prepare again.")
	}
	var trueBin string
	if witness.level != "microvm" {
		resolved, err := resolveSandboxPrepareTrueV1()
		if err != nil {
			return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, "Ensure /usr/bin/true or /bin/true is installed on the host.")
		}
		trueBin = resolved
	}

	reservation, err := reserveSessionID("")
	if err != nil {
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, defaultHint)
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		reservation.close()
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, defaultHint)
	}

	m, err := buildSandboxPrepareManifestV1(ctx, reservation.id, trueBin, witness)
	if err != nil {
		b.joinSandboxPrepareDirectoryCleanupV1(operation, reservation)
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, defaultHint)
	}
	if err := bindAndSaveSandboxPrepareV1(operation, reservation, m, witness); err != nil {
		b.joinSandboxPrepareDirectoryCleanupV1(operation, reservation)
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, defaultHint)
	}
	if err := witness.releaseImageLease(); err != nil {
		b.joinSandboxPrepareDirectoryCleanupV1(operation, reservation)
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(err, defaultHint)
	}

	if sandboxPrepareBootOverrideV1 != nil {
		if hooked, ok := sandboxPrepareBootOverrideV1(ctx, operation, reservation, m); ok {
			return finishHookedSandboxPrepareV1(ctx, operation, reservation, m, witness, hooked, defaultHint)
		}
	}

	bootStart := time.Now()
	_, launch, bootErr := startSandboxPrepareSessionV1(ctx, operation)
	bootMS := max(time.Since(bootStart).Milliseconds(), 0)
	if launch != nil {
		reservation.launched = launch.started
		m.launchStarted = launch.started
		m.Run.Unit, m.Run.HolderPID = launch.manifest.Run.Unit, launch.manifest.Run.HolderPID
	}

	// The RPC timer can already have sent its one failure. This SAME request
	// worker still owns all actual objects and keeps joining cleanup under
	// the original operation lifetime, without renewing construction authority.
	cleanupCtx := sandboxPrepareCleanupContextV1(operation)
	for {
		stopErr := stopAndJoinSandboxPrepareLaunchV1(cleanupCtx, launch, m.ID, m)
		if stopErr == nil {
			break
		}
		if b.log != nil {
			diagnostic := error(nil)
			if launch != nil {
				diagnostic = launch.prepareCleanupDiagnostic
			}
			b.log.Warn("fixed test retains its original native cleanup owner", "session", m.ID, "err", errors.Join(stopErr, diagnostic))
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.joinSandboxPrepareDirectoryCleanupV1(operation, reservation)
	if launch != nil && launch.prepareCleanupDiagnostic != nil {
		return api.SandboxPrepareResultV1{}, sandboxBootError("the sandbox test boot reported a cleanup I/O failure: "+errors.Join(bootErr, launch.prepareCleanupDiagnostic).Error(), defaultHint)
	}
	if bootErr != nil {
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(bootErr, defaultHint)
	}
	if err := ctx.Err(); err != nil {
		return api.SandboxPrepareResultV1{}, sandboxBootError("the sandbox test boot was cancelled before completion: "+err.Error(), defaultHint)
	}
	return api.SandboxPrepareResultV1{
		Version: 1,
		Level:   witness.level,
		Driver:  witness.driver,
		BootMS:  bootMS,
	}, nil
}

func finishHookedSandboxPrepareV1(ctx context.Context, operation *SessionOperation, reservation *sessionReservation, m *Manifest, witness *sandboxPrepareWitnessV1, hooked *sandboxPrepareHookResultV1, defaultHint string) (api.SandboxPrepareResultV1, error) {
	if hooked != nil && hooked.stopUnconfirmedErr != nil {
		// The fixture must retain its actual injected owner in this SAME
		// original request stack, just as the real native path does above.
		<-hooked.stopConfirmed
	}
	cleanupErr := cleanupSandboxPrepareSessionV1(operation, reservation)
	if cleanupErr != nil {
		return api.SandboxPrepareResultV1{}, sandboxBootError("sandbox test boot cleanup failed: "+cleanupErr.Error(), defaultHint)
	}
	if hooked != nil && hooked.bootErr != nil {
		return api.SandboxPrepareResultV1{}, wrapSandboxBootError(hooked.bootErr, defaultHint)
	}
	if err := ctx.Err(); err != nil {
		return api.SandboxPrepareResultV1{}, sandboxBootError("the sandbox test boot was cancelled before completion: "+err.Error(), defaultHint)
	}
	var bootMS int64
	if hooked != nil {
		bootMS = max(hooked.bootMS, 0)
	}
	return api.SandboxPrepareResultV1{
		Version: 1,
		Level:   witness.level,
		Driver:  witness.driver,
		BootMS:  bootMS,
	}, nil
}

func probeSandboxPrepareDriverV1(ctx context.Context, b *bridge, witness *sandboxPrepareWitnessV1) error {
	if sandboxPrepareProbeOverrideV1 != nil {
		return sandboxPrepareProbeOverrideV1(ctx, b, witness)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	switch witness.level {
	case "process":
		p := process.Probe(probeCtx)
		if !p.OK {
			return sandboxBootError("the process sandbox is not runnable on this host: "+p.Reason, probeFixHintV1(p.Fix))
		}
		return nil
	case "gvisor":
		if witness.runtime == nil {
			return newRuntimeError("the gvisor test boot has no selected runtime")
		}
		purposeCtx, finish, err := newGvisorProbePurposeV1(probeCtx, b, witness.requestID, witness.runtime, witness.runtime.manifest)
		if err != nil {
			return err
		}
		var p wall.Probe
		registered := false
		for _, candidate := range wall.Drivers() {
			if candidate.Name() == "gvisor" {
				p = candidate.Probe(purposeCtx)
				registered = true
				break
			}
		}
		finishErr := finish()
		if err := errors.Join(finishErr); err != nil {
			return err
		}
		if !registered {
			return sandboxBootError("the gvisor driver is unavailable in this host helper", "Upgrade the host helper and run "+brand.Name+" host prepare again.")
		}
		if !p.OK {
			return sandboxBootError("the gvisor sandbox is not runnable on this host: "+p.Reason, probeFixHintV1(p.Fix))
		}
		return nil
	case "microvm":
		probe, err := CapabilityProbe(probeCtx, "")
		if err != nil {
			return err
		}
		if !probe.KVM.Usable {
			return sandboxBootError("KVM is not usable on this host for microvm preparation", "Grant /dev/kvm access with "+brand.Name+" host prepare, or choose a supported wall.")
		}
		for _, candidate := range probe.Walls {
			if candidate.Driver == witness.driver && candidate.Level == "microvm" {
				if !candidate.OK && !candidate.CanPrepare {
					reason := candidate.Reason
					if reason == "" {
						reason = "the driver probe reported not ready"
					}
					return sandboxBootError("the "+witness.driver+" microvm driver is not runnable on this host: "+reason, probeFixHintV1(candidate.Fix))
				}
				return nil
			}
		}
		return sandboxBootError("the "+witness.driver+" microvm driver is not registered on this host", "Run "+brand.Name+" host doctor and prepare a supported microvm driver.")
	default:
		return sandboxBootError("unsupported sandbox preparation level", "Use process, gvisor or microvm.")
	}
}

func probeFixHintV1(fix string) string {
	fix = strings.TrimSpace(fix)
	if fix != "" {
		return fix
	}
	return "Run " + brand.Name + " host doctor and complete the required host steps."
}

func checkSandboxPrepareDiskV1(witness *sandboxPrepareWitnessV1) error {
	needSessionBytes := api.SandboxPrepareDiskV1 + sandboxPrepareScratchSlackV1
	sessionFree, err := sandboxPrepareDiskFreeV1(paths.SessionsDir())
	if err != nil {
		return fmt.Errorf("check session storage capacity: %w", err)
	}
	if sessionFree < needSessionBytes {
		return sandboxBootError(
			fmt.Sprintf("insufficient free disk space for the 1 GiB test sandbox (%s available, %s needed)", gibText(sessionFree), gibText(needSessionBytes)),
			"Free disk space in "+paths.StateDir()+", then run "+brand.Name+" host prepare again.",
		)
	}
	if witness.level == "microvm" {
		if witness.imageSize <= 0 || witness.agentsSize <= 0 {
			return sandboxBootError("the microvm base image or installed-agents disk size is invalid", "Pull the default image and rebuild the agents disk before retrying.")
		}
		dataFree, err := sandboxPrepareDiskFreeV1(paths.DataDir())
		if err != nil {
			return fmt.Errorf("check data storage capacity: %w", err)
		}
		if dataFree < sandboxPrepareScratchSlackV1 {
			return sandboxBootError(
				fmt.Sprintf("insufficient free disk space in %s (%s available)", paths.DataDir(), gibText(dataFree)),
				"Free disk space on the host, then run "+brand.Name+" host prepare again.",
			)
		}
	}
	return nil
}

func statfsFreeBytesV1(target string) (int64, error) {
	path := filepath.Clean(target)
	var st unix.Statfs_t
	for unix.Statfs(path, &st) != nil {
		parent := filepath.Dir(path)
		if parent == path {
			return 0, fmt.Errorf("statfs %s failed", target)
		}
		path = parent
	}
	if st.Bsize <= 0 || st.Bavail > uint64(math.MaxInt64)/uint64(st.Bsize) {
		return 0, fmt.Errorf("invalid filesystem block counts for %s", target)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

func resolveSandboxPrepareTrueV1() (string, error) {
	bin, err := lookPath("true", sandboxPrepareHostPathV1)
	if err != nil {
		return "", err
	}
	binInfo, err := os.Lstat(bin)
	if err != nil {
		return "", fmt.Errorf("stat true binary %q: %w", bin, err)
	}
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", fmt.Errorf("resolve true binary %q: %w", bin, err)
	}
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat resolved true binary %q: %w", resolved, err)
	}
	if err := validateSandboxPrepareTrueV1(bin, binInfo, resolved, resolvedInfo); err != nil {
		return "", err
	}
	return bin, nil
}

func validateSandboxPrepareTrueV1(bin string, binInfo os.FileInfo, resolved string, resolvedInfo os.FileInfo) error {
	if binInfo == nil || resolvedInfo == nil {
		return fmt.Errorf("unexpected true binary path %q", resolved)
	}
	binStat, ok := binInfo.Sys().(*syscall.Stat_t)
	if !ok || binStat.Uid != 0 {
		return fmt.Errorf("untrusted true binary %q", bin)
	}
	if binInfo.Mode()&os.ModeSymlink == 0 && (!binInfo.Mode().IsRegular() || binInfo.Mode().Perm()&0o022 != 0 || binInfo.Mode()&0o111 == 0) {
		return fmt.Errorf("untrusted true binary %q", bin)
	}
	if !isTrustedSystemTruePathV1(resolved) {
		return fmt.Errorf("unexpected true binary path %q", resolved)
	}
	resStat, ok := resolvedInfo.Sys().(*syscall.Stat_t)
	if !ok || resStat.Uid != 0 || !resolvedInfo.Mode().IsRegular() || resolvedInfo.Mode().Perm()&0o022 != 0 || resolvedInfo.Mode()&0o111 == 0 {
		return fmt.Errorf("untrusted resolved true binary %q", resolved)
	}
	return nil
}

func isTrustedSystemTruePathV1(resolved string) bool {
	clean := filepath.Clean(resolved)
	if !filepath.IsAbs(clean) {
		return false
	}
	trustedDir := false
	for _, prefix := range []string{"/usr/", "/bin/", "/lib/", "/lib64/", "/nix/store/"} {
		if strings.HasPrefix(clean, prefix) {
			trustedDir = true
			break
		}
	}
	if !trustedDir {
		return false
	}
	base := filepath.Base(clean)
	switch base {
	case "true", "coreutils", "busybox", "toybox", "gnutrue":
		return true
	}
	return strings.HasSuffix(base, "-true") && len(base) > len("-true")
}
