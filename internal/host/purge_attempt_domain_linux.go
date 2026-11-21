//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This producer derives THIS protected process's existing host domain from
// its actual kernel image and fresh native descriptors. Incoming code/parent
// observations, root DTOs and code-file data cannot construct that domain.
// Its placement is installed only after the actual original DATA parent has
// passed the closed operator boundary. It accepts no resource authority.
type purgeAttemptDomainV3 struct {
	mu        sync.RWMutex
	image     *purgeAttemptImageV2
	placement *placement
	control   *os.Root
	state     *os.Root
	lock      *os.File
	host      *os.File
	place     *os.File
	closed    bool
	closeErr  error
}

var purgeAttemptDomainsV3 = struct {
	sync.Mutex
	owners map[*placement]*purgeAttemptDomainV3
}{owners: map[*placement]*purgeAttemptDomainV3{}}

// Only the independently installed current-process producer can supply the
// actual copied image to closed compatibility consumers. Request fields,
// filenames, public descriptors and a placement-shaped value cannot do so.
func purgeAttemptImageForPlacementV3(ctx context.Context, p *placement) (*purgeAttemptImageV2, error) {
	purgeAttemptDomainsV3.Lock()
	owner := purgeAttemptDomainsV3.owners[p]
	purgeAttemptDomainsV3.Unlock()
	if owner == nil {
		return nil, placementError("the copied purge consumer has no independently installed actual domain")
	}
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	if owner.closed || owner.placement != p || owner.image == nil {
		return nil, placementError("the copied purge consumer has no independently installed actual domain")
	}
	if err := owner.image.Check(ctx); err != nil {
		return nil, err
	}
	return owner.image, nil
}

func newIndependentPurgeAttemptDomainV3(ctx context.Context, image *purgeAttemptImageV2, args api.PurgeArgs) (result *purgeAttemptDomainV3, returned error) {
	if ctx == nil || image == nil || !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) {
		return nil, placementError("the protected purge process has no current image and exact host request")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), image.Check(ctx)); err != nil {
		return nil, err
	}
	owner := &purgeAttemptDomainV3{image: image}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	scope, controlPath, err := placementScope()
	if err != nil {
		return nil, err
	}
	// These are own current-process values. This method creates no directory,
	// host ID, lock file, installation record, reservation or session object.
	name, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if filepath.Base(name) != brand.HostBinary || !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, placementError("the current purge image has no exact native helper name")
	}
	if _, err := mappedExecutableMatches(name, image.mapped); err != nil {
		return nil, err
	}
	installation, err := cachefs.OpenPrivateRoot(filepath.Dir(name), false)
	if err != nil {
		return nil, err
	}
	file, err := installation.Open(".")
	if err != nil {
		return nil, errors.Join(err, installation.Close())
	}
	installInfo, err := file.Stat()
	closed := errors.Join(file.Close(), installation.Close())
	if err != nil || closed != nil {
		return nil, errors.Join(err, closed)
	}
	owner.control, err = cachefs.OpenPrivateRoot(controlPath, false)
	if err != nil {
		return nil, err
	}
	controlInfo, err := nativeWorktreeDirectoryRefV1(owner.control)
	if err != nil {
		return nil, err
	}
	owner.lock, err = cachefs.OpenFile(owner.control, "control.lock", os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lockInfo, err := cachefs.ValidateFile(owner.control, "control.lock", owner.lock, 0o600)
	if err != nil {
		return nil, err
	}
	unlock, err := waitPlacementFile(ctx, owner.control, "control.lock", owner.lock, lockInfo)
	if err != nil {
		return nil, err
	}
	defer unlock()
	control, found, err := readPlacementControl(owner.control, scope)
	if err != nil || !found {
		return nil, errors.Join(placementError("the independent purge process found no original existing host domain"), err)
	}
	if control.Destructive || len(control.Sessions) != 0 || control.Phase == "finalising" {
		return nil, placementError("a fresh protected process cannot adopt the saved destructive attempt's missing custody")
	}
	if control.HostID != args.HostID || !samePersistentRootBinding(control.PersistentRoots, currentPersistentRoots()) || !samePurgeDirectoryRef(control.Control, controlInfo) || !control.ControlLock.matches(lockInfo) {
		return nil, placementError("the exact request differs from the current native host domain")
	}
	owner.state, err = cachefs.OpenPrivateRoot(control.PersistentRoots.State, false)
	if err != nil {
		return nil, err
	}
	stateInfo, err := nativeWorktreeDirectoryRefV1(owner.state)
	if err != nil || !samePurgeDirectoryRef(control.State, stateInfo) {
		return nil, errors.Join(placementError("the original host state generation changed"), err)
	}
	owner.host, err = cachefs.OpenFile(owner.state, "host-id", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	hostInfo, err := cachefs.ValidateFile(owner.state, "host-id", owner.host, 0o600)
	if err != nil || !control.Host.matches(hostInfo) {
		return nil, errors.Join(placementError("the original host identity descriptor changed"), err)
	}
	hostID, err := readHostIDDescriptor(owner.host)
	if err != nil || hostID != args.HostID {
		return nil, errors.Join(placementError("the actual host identity does not match this request"), err)
	}
	owner.place, err = cachefs.OpenFile(owner.state, "place.lock", os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	placeInfo, err := cachefs.ValidateFile(owner.state, "place.lock", owner.place, 0o600)
	if err != nil || !control.Place.matches(placeInfo) {
		return nil, errors.Join(placementError("the original placement sentinel changed"), err)
	}
	domain := cachefs.Domain{Scope: scope, HostID: args.HostID}
	actual, bound, err := cachefs.ReadDomainBinding(ctx, owner.state, "place.lock", owner.place)
	if err != nil || !bound || actual != domain {
		return nil, errors.Join(placementError("the original placement description has another native domain binding"), err)
	}
	p := &placement{scope: scope, control: controlPath, executable: name, executableRef: image.mapped, installRef: installInfo, stateRef: sourceInfoFromRootV3(owner.state), hostRef: hostInfo, placeRef: placeInfo, controlRef: sourceInfoFromRootV3(owner.control), controlLockRef: lockInfo, hostID: args.HostID, boundRoots: control.PersistentRoots}
	if p.stateRef == nil || p.controlRef == nil {
		return nil, placementError("the independent current domain lost a real native directory description")
	}
	if err := errors.Join(p.checkIdentity(), image.Check(ctx)); err != nil {
		return nil, err
	}
	owner.placement = p
	complete = true
	return owner, nil
}

func sourceInfoFromRootV3(root *os.Root) os.FileInfo {
	file, err := root.Open(".")
	if err != nil {
		return nil
	}
	info, err := file.Stat()
	closed := file.Close()
	if err != nil || closed != nil {
		return nil
	}
	return info
}

// Install binds only this independently derived actual placement in this
// current protected process. It does not accept a caller-supplied placement.
func (owner *purgeAttemptDomainV3) Install(ctx context.Context) error {
	if owner == nil {
		return placementError("the independently derived purge domain is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if ctx == nil || owner.closed || owner.image == nil || owner.placement == nil {
		return placementError("the actual independently derived purge domain is absent")
	}
	if err := errors.Join(ctx.Err(), owner.image.Check(ctx), owner.placement.checkIdentity()); err != nil {
		return err
	}
	placementProcesses.Lock()
	defer placementProcesses.Unlock()
	if placementProcesses.entry != nil && placementProcesses.entry != owner.placement {
		return placementError("the protected process already has another actual placement source")
	}
	if prior := placementProcesses.byScope[owner.placement.scope]; prior != nil && prior != owner.placement {
		return placementError("the protected process already captured another native scope object")
	}
	placementProcesses.entry, placementProcesses.byScope[owner.placement.scope] = owner.placement, owner.placement
	purgeAttemptDomainsV3.Lock()
	defer purgeAttemptDomainsV3.Unlock()
	if prior := purgeAttemptDomainsV3.owners[owner.placement]; prior != nil && prior != owner {
		return placementError("the actual purge placement already has another current image owner")
	}
	purgeAttemptDomainsV3.owners[owner.placement] = owner
	return nil
}

func (owner *purgeAttemptDomainV3) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		if err := observeOriginalPurgeDescriptorCloseV3([]*os.File{owner.lock, owner.host, owner.place}, []*os.Root{owner.control, owner.state}); err != nil {
			return err
		}
		return nil
	}
	purgeAttemptDomainsV3.Lock()
	if purgeAttemptDomainsV3.owners[owner.placement] == owner {
		delete(purgeAttemptDomainsV3.owners, owner.placement)
	}
	purgeAttemptDomainsV3.Unlock()
	for _, file := range []*os.File{owner.lock, owner.host, owner.place} {
		if file != nil {
			owner.closeErr = errors.Join(owner.closeErr, file.Close())
		}
	}
	for _, root := range []*os.Root{owner.control, owner.state} {
		if root != nil {
			owner.closeErr = errors.Join(owner.closeErr, root.Close())
		}
	}
	owner.closed = true
	return owner.closeErr
}
