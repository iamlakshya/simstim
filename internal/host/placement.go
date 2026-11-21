package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

type placement struct {
	publicationCleanup placementPublicationCleanupV1
	scope              string
	control            string
	executable         string
	executableRef      os.FileInfo
	installRef         os.FileInfo
	stateRef           os.FileInfo
	hostRef            os.FileInfo
	placeRef           os.FileInfo
	controlRef         os.FileInfo
	controlLockRef     os.FileInfo
	hostID             string
	boundRoots         persistentRootBinding
	cleanupKind        string
	installer          bool
	installerCreated   map[string]purgeRef
}

var placementProcesses = struct {
	sync.Mutex
	entry   *placement
	byScope map[string]*placement
}{byScope: map[string]*placement{}}

func placementScope() (string, string, error) {
	roots := []string{paths.StateDir(), paths.DataDir(), paths.ConfigDir()}
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return "", "", placementError("the configured host roots are not absolute and clean")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", "", placementError("cannot identify the persistent login home")
	}
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil || canonical != filepath.Clean(home) {
		return "", "", placementError("HOME must name its canonical absolute directory before host admission")
	}
	sum := sha256.Sum256([]byte("placement-domain-v2\x00" + fmt.Sprint(os.Getuid()) + "\x00" + canonical))
	scope := hex.EncodeToString(sum[:])
	control := filepath.Join(home, "."+brand.Name+"-purge-"+scope)
	for _, root := range roots {
		rel, err := filepath.Rel(root, control)
		if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", "", placementError("the purge control would lie inside a removable host root")
		}
	}
	return scope, control, nil
}

func placementError(message string) error {
	return UserError("placement_identity", message, "Keep the host saved and inspect its installation identity and purge control before trying again.")
}

// InitPlacement pins this production process before it creates a host ID or
// handles any request. Every supported helper command calls it at entry.
// The closed one-shot cleanup command uses the retained guard instead.
func InitPlacement() error { return InitPlacementContext(context.Background()) }

// InitPlacementContext is the cancellable production entry admission. A hook
// uses its existing short deadline so it cannot wait behind a long publisher.
func InitPlacementContext(ctx context.Context) error {
	p, err := placementForPurposeContext(ctx, false)
	if err != nil {
		return err
	}
	if p.installer {
		return placementError("an installer payload cannot enter normal helper command dispatch")
	}
	placementProcesses.Lock()
	defer placementProcesses.Unlock()
	if placementProcesses.entry != nil && placementProcesses.entry != p {
		return placementError("the process changed its configured root scope")
	}
	placementProcesses.entry = p
	return nil
}

func placementForCall() (*placement, error) { return placementForPurpose(false) }

// placementForInstall is available only after closed-purpose self-verification.
func placementForInstall() (*placement, error) {
	p, err := placementForPurpose(true)
	if err != nil {
		return nil, err
	}
	placementProcesses.Lock()
	defer placementProcesses.Unlock()
	if placementProcesses.entry != nil && placementProcesses.entry != p {
		return nil, placementError("the installer changed process root scope")
	}
	p.installer = true
	placementProcesses.entry = p
	return p, nil
}

func placementForPurpose(installer bool) (*placement, error) {
	return placementForPurposeContext(context.Background(), installer)
}
func placementForPurposeContext(ctx context.Context, installer bool) (*placement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope, control, err := placementScope()
	if err != nil {
		return nil, err
	}
	for !placementProcesses.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	defer placementProcesses.Unlock()
	if entry := placementProcesses.entry; entry != nil {
		if !samePersistentRootBinding(entry.boundRoots, currentPersistentRoots()) {
			return nil, UserError("root_configuration", "the process changed its recorded persistent roots", "Restore the recorded XDG roots before retrying the same operation.")
		}
		if entry.scope != scope {
			return nil, placementError("the process changed its configured host roots")
		}
		return entry, nil
	}
	if old := placementProcesses.byScope[scope]; old != nil {
		if !samePersistentRootBinding(old.boundRoots, currentPersistentRoots()) {
			return nil, UserError("root_configuration", "this home is already bound to another persistent root set", "Restore its recorded XDG roots or finish its removal before changing them.")
		}
		return old, nil
	}
	p := &placement{scope: scope, control: control, installer: installer}
	if err := p.initialiseContext(ctx); err != nil {
		return nil, err
	}
	placementProcesses.byScope[scope] = p
	return p, nil
}

func (p *placement) initialise() error { return p.initialiseContext(context.Background()) }
func (p *placement) initialiseContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exe, info, err := mappedExecutable()
	if err != nil {
		return err
	}
	if isPurgePayloadPath(exe) && (!p.installer || !isInstallPayloadPath(exe)) {
		return placementError("a one-shot purge executable accepts only its closed cleanup command")
	}
	install, err := cachefs.OpenPrivateRoot(filepath.Dir(exe), false)
	if err != nil {
		return placementError("cannot anchor the running helper's installation directory")
	}
	installDir, err := install.Open(".")
	if err == nil {
		p.installRef, err = installDir.Stat()
		installDir.Close()
	}
	install.Close()
	if err != nil {
		return placementError("cannot pin the helper installation identity")
	}
	p.executable, p.executableRef = exe, info
	return withPlacementHome(ctx, func(home *os.Root) error {
		if err := refuseFinalisingEntry(home, p.scope, filepath.Base(p.control)); err != nil {
			return err
		}
		created := false
		if err := home.Mkdir(filepath.Base(p.control), 0o700); err == nil {
			created = true
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
		err := p.initialiseDomain(ctx, created)
		if err != nil && created {
			err = errors.Join(err, p.cleanupUninitialisedGuard(home))
		}
		return err
	})
}

func (p *placement) initialiseDomain(ctx context.Context, created bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := cachefs.OpenPrivateRoot(p.control, false)
	if err != nil {
		return placementError("cannot anchor the purge control: " + err.Error())
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	p.controlRef, err = dir.Stat()
	dir.Close()
	if err != nil {
		return err
	}
	if p.controlRef.Mode().Perm() != 0o700 {
		return placementError("the purge control directory is not private")
	}
	if _, err := root.Lstat("control.json"); !created && errors.Is(err, os.ErrNotExist) {
		return placementError("an existing guard lost its control receipt and cannot create a new lock domain")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lockFlags := os.O_RDWR
	if created {
		lockFlags |= os.O_CREATE | os.O_EXCL
	}
	lock, err := cachefs.OpenFile(root, "control.lock", lockFlags, 0o600)
	if err != nil {
		return placementError("cannot establish the purge control lock")
	}
	defer lock.Close()
	p.controlLockRef, err = lock.Stat()
	if err != nil {
		return err
	}
	if created {
		if err := lock.Sync(); err != nil {
			return err
		}
		if err := syncCapturedDirectory(root); err != nil {
			return err
		}
	}
	unlock, err := waitPlacementFile(ctx, root, "control.lock", lock, p.controlLockRef)
	if err != nil {
		return err
	}
	defer unlock()
	control, found, err := readPlacementControl(root, p.scope)
	if err != nil {
		return err
	}
	if found {
		p.boundRoots = control.PersistentRoots
	} else {
		p.boundRoots = currentPersistentRoots()
	}
	if found && control.ShutdownCommitted != nil {
		if err := reconcileIdleShutdownCommit(&control, BootID()); err != nil {
			return err
		}
		if err := writePlacementControl(root, control); err != nil {
			return err
		}
	}
	if found && control.Drain == "purge" {
		p.hostID = control.HostID
		// A bridge arriving during a precheck pins the existing generation
		// without creating anything. If that precheck refuses, it can use
		// the same generation after the owned drain is withdrawn.
		for _, item := range []struct {
			path string
			ref  purgeRef
			into *os.FileInfo
		}{
			{paths.StateDir(), control.State, &p.stateRef}, {paths.HostID(), control.Host, &p.hostRef}, {filepath.Join(paths.StateDir(), "place.lock"), control.Place, &p.placeRef},
		} {
			if current, err := os.Lstat(item.path); err == nil && item.ref.matches(current) {
				*item.into = current
			}
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var state *os.Root
	if p.installer && !found {
		state, err = cachefs.OpenPrivateRootCreated(paths.StateDir(), p.observeInstallerDirectoryV1)
	} else {
		state, err = cachefs.OpenPrivateRoot(paths.StateDir(), !found)
	}
	if err != nil {
		return placementError("cannot anchor the host state generation")
	}
	defer state.Close()
	stateDir, err := state.Open(".")
	if err != nil {
		return err
	}
	p.stateRef, err = stateDir.Stat()
	stateDir.Close()
	if err != nil {
		return err
	}
	host, err := cachefs.OpenFile(state, "host-id", os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) && !found {
		if _, err := createHostID(paths.StateDir(), paths.HostID()); err != nil {
			return err
		}
		host, err = cachefs.OpenFile(state, "host-id", os.O_RDONLY, 0o600)
	}
	if err != nil {
		return placementError("cannot pin the host ID generation")
	}
	p.hostRef, err = host.Stat()
	id, readErr := readHostIDDescriptor(host)
	host.Close()
	if err != nil || readErr != nil {
		return errors.Join(err, readErr)
	}
	p.hostID = id
	placeFlags := os.O_RDWR
	if !found {
		placeFlags |= os.O_CREATE | os.O_EXCL
	}
	place, err := cachefs.OpenFile(state, "place.lock", placeFlags, 0o600)
	placeCreated := err == nil && !found
	if errors.Is(err, os.ErrExist) && !found {
		place, err = cachefs.OpenFile(state, "place.lock", os.O_RDWR, 0o600)
	}
	if err != nil {
		return placementError("cannot establish the placement lock")
	}
	p.placeRef, err = place.Stat()
	if err == nil && placeCreated {
		err = place.Sync()
		if err == nil {
			err = syncCapturedDirectory(state)
		}
	}
	if err == nil {
		err = p.bindStateDomain(ctx, state, place, placeCreated)
	}
	place.Close()
	if err != nil {
		return err
	}
	if found {
		if control.HostID != id || !control.State.matches(p.stateRef) || !control.Host.matches(p.hostRef) || !control.Place.matches(p.placeRef) {
			return placementError("the retained control belongs to another host generation")
		}
		return nil
	}
	runtimeRoots, err := initialRuntimeRoots()
	if err != nil {
		return err
	}
	control = placementControl{ScopeFormat: 2, PersistentRoots: currentPersistentRoots(), RuntimeRoots: runtimeRoots, Version: 1, Scope: p.scope, UID: os.Getuid(), Control: refOf(p.controlRef), ControlLock: refOf(p.controlLockRef), HostID: id, State: refOf(p.stateRef), Host: refOf(p.hostRef), Place: refOf(p.placeRef), Active: map[string]placementOperationRecord{}, Refs: map[string]purgeRef{"state/host-id": refOf(p.hostRef), "state/place.lock": refOf(p.placeRef)}}
	if p.installer {
		if err := p.commitInstallerDirectoriesV1(&control); err != nil {
			return err
		}
	}
	return writePlacementControl(root, control)
}

func (p *placement) checkIdentity() error {
	install, err := cachefs.OpenPrivateRoot(filepath.Dir(p.executable), false)
	if err != nil {
		return placementError("the helper installation directory changed")
	}
	dir, err := install.Open(".")
	if err != nil {
		install.Close()
		return placementError("cannot inspect the original installation directory")
	}
	info, err := dir.Stat()
	dir.Close()
	install.Close()
	if err != nil || p.installRef == nil || !os.SameFile(p.installRef, info) {
		return placementError("the helper installation directory changed identity")
	}

	if _, err := mappedExecutableMatches(p.executable, p.executableRef); err != nil {
		return err
	}
	for _, item := range []struct {
		path string
		ref  os.FileInfo
	}{{paths.StateDir(), p.stateRef}, {paths.HostID(), p.hostRef}, {filepath.Join(paths.StateDir(), "place.lock"), p.placeRef}} {
		current, err := os.Lstat(item.path)
		if err != nil || item.ref == nil || !os.SameFile(item.ref, current) || !refOf(item.ref).matches(current) {
			return placementError("the host root or placement generation changed")
		}
	}
	return nil
}

// CheckStart refuses early preparation while purge drains this root scope.
// Final actual starts and publications must also use withPlacement.
func CheckStart() error { return CheckStartContext(context.Background()) }

// CheckStartContext refuses early work with the caller's admission deadline.
func CheckStartContext(ctx context.Context) error {
	p, err := placementForPurposeContext(ctx, false)
	if err != nil {
		return err
	}
	return p.withControl(ctx, func(control *placementControl) error {
		if err := reconcileIdleShutdownCommit(control, BootID()); err != nil {
			return err
		}
		if control.Drain != "" {
			return UserError("draining", "this host is draining for "+control.Drain, "Wait for the removal to finish before starting another session.")
		}
		return p.checkIdentity()
	}, false)
}

func isPurgePayloadPath(executable string) bool {
	for dir := filepath.Dir(executable); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if strings.HasPrefix(filepath.Base(dir), "."+brand.Name+"-purge-") || strings.HasPrefix(filepath.Base(dir), "."+brand.Name+"-install-") {
			return true
		}
	}
	return false
}

func isInstallPayloadPath(executable string) bool {
	home, err := os.UserHomeDir()
	return err == nil && filepath.Dir(filepath.Dir(executable)) == filepath.Clean(home) && strings.HasPrefix(filepath.Base(filepath.Dir(executable)), "."+brand.Name+"-install-")
}
