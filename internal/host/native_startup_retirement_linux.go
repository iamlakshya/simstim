//go:build linux

package host

import (
	"errors"
	"net"
	"os"
	"sync"
)

// Existing native peer, identity and lease teardown contains no Purge or
// other operation authority. Each caller retains its real producer and joins
// its own borrowers before using this resource plumbing.
type nativeStartupRetirementsV1 struct {
	core       *nativeDescriptorRetirementsV1
	mu         sync.Mutex
	peers      map[*nativeStartupPeerV1]*nativePeerRetirementV1
	identities map[*nativeHelperIdentityV1]*nativeIdentityRetirementV1
	leases     map[*sessionFileLease]*nativeLeaseRetirementV1
}

func (owner *nativeStartupRetirementsV1) descriptorOwnerV4() *nativeDescriptorRetirementsV1 {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.core == nil {
		owner.core = &nativeDescriptorRetirementsV1{}
	}
	return owner.core
}

// This binds only resource ownership from the actual immutable first reader.
// It transfers no Source or role and never copies or replaces native facts.
func (owner *nativeStartupRetirementsV1) bindDescriptorOwnerV4(core *nativeDescriptorRetirementsV1) error {
	if core == nil {
		return placementError("the native resource pool is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.core != nil && owner.core != core {
		return placementError("the original native resource pool cannot be replaced")
	}
	owner.core = core
	return nil
}

func (owner *nativeStartupRetirementsV1) closeFilesV4(files []*os.File) error {
	return owner.descriptorOwnerV4().closeFilesV4(files)
}
func (owner *nativeStartupRetirementsV1) checkFilesV4(files []*os.File) error {
	return owner.descriptorOwnerV4().checkFilesV4(files)
}
func (owner *nativeStartupRetirementsV1) closeRootsV4(roots []*os.Root) error {
	return owner.descriptorOwnerV4().closeRootsV4(roots)
}
func (owner *nativeStartupRetirementsV1) checkRootsV4(roots []*os.Root) error {
	return owner.descriptorOwnerV4().checkRootsV4(roots)
}
func (owner *nativeStartupRetirementsV1) closeConnectionV4(connection *net.UnixConn) error {
	return owner.descriptorOwnerV4().closeConnectionV4(connection)
}
func (owner *nativeStartupRetirementsV1) checkConnectionV4(connection *net.UnixConn) error {
	return owner.descriptorOwnerV4().checkConnectionV4(connection)
}
func (owner *nativeStartupRetirementsV1) closeListenerV4(listener *net.UnixListener) error {
	return owner.descriptorOwnerV4().closeListenerV4(listener)
}
func (owner *nativeStartupRetirementsV1) checkListenerV4(listener *net.UnixListener) error {
	return owner.descriptorOwnerV4().checkListenerV4(listener)
}
func (owner *nativeStartupRetirementsV1) diagnosticsV4() error {
	return owner.descriptorOwnerV4().diagnosticsV4()
}

type nativeIdentityRetirementV1 struct {
	identity *nativeHelperIdentityV1
	files    []*os.File
	unknown  error
}

type nativePeerRetirementV1 struct {
	mu       sync.Mutex
	peer     *nativeStartupPeerV1
	pidfd    int
	handle   *os.File
	proc     *os.Root
	identity *nativeHelperIdentityV1
	unknown  error
}

type nativeLeaseRetirementV1 struct {
	mu      sync.Mutex
	lease   *sessionFileLease
	file    *os.File
	root    *os.Root
	unknown error
}

func (owner *nativeStartupRetirementsV1) checkAllRetiredV4() error {
	if err := owner.descriptorOwnerV4().checkAllRetiredV4(); err != nil {
		return err
	}
	owner.mu.Lock()
	peers := make([]*nativeStartupPeerV1, 0, len(owner.peers))
	for peer := range owner.peers {
		peers = append(peers, peer)
	}
	identities := make([]*nativeHelperIdentityV1, 0, len(owner.identities))
	for identity := range owner.identities {
		identities = append(identities, identity)
	}
	leases := make([]*sessionFileLease, 0, len(owner.leases))
	for lease := range owner.leases {
		leases = append(leases, lease)
	}
	owner.mu.Unlock()
	for _, peer := range peers {
		if err := owner.checkPeerV4(peer); err != nil {
			return err
		}
	}
	for _, identity := range identities {
		if err := owner.checkIdentityV4(identity); err != nil {
			return err
		}
	}
	for _, lease := range leases {
		if err := owner.checkSessionLeaseV4(lease); err != nil {
			return err
		}
	}
	return nil
}

func (owner *nativeStartupRetirementsV1) closeIdentityV4(identity *nativeHelperIdentityV1) error {
	if identity == nil {
		return nil
	}
	identity.mu.Lock()
	owner.mu.Lock()
	if owner.identities == nil {
		owner.identities = make(map[*nativeHelperIdentityV1]*nativeIdentityRetirementV1)
	}
	record := owner.identities[identity]
	if record == nil {
		record = &nativeIdentityRetirementV1{identity: identity, files: []*os.File{identity.executable, identity.user, identity.cgroup}}
		owner.identities[identity] = record
		if identity.closed {
			record.unknown = placementError("the original native identity was closed without retained first native facts")
		} else {
			identity.closed = true
		}
	}
	owner.mu.Unlock()
	identity.mu.Unlock()
	if record.unknown != nil {
		return record.unknown
	}
	return owner.closeFilesV4(record.files)
}

func (owner *nativeStartupRetirementsV1) checkIdentityV4(identity *nativeHelperIdentityV1) error {
	if identity == nil {
		return nil
	}
	owner.mu.Lock()
	record := owner.identities[identity]
	owner.mu.Unlock()
	if record == nil || record.identity != identity || record.unknown != nil || len(record.files) != 3 || record.files[0] != identity.executable || record.files[1] != identity.user || record.files[2] != identity.cgroup {
		return errors.Join(placementError("the original identity has no same-object native retirement"), recordUnknownIdentityV4(record))
	}
	return owner.checkFilesV4(record.files)
}

func recordUnknownIdentityV4(record *nativeIdentityRetirementV1) error {
	if record == nil {
		return nil
	}
	return record.unknown
}

func (owner *nativeStartupRetirementsV1) closePeerV4(peer *nativeStartupPeerV1) error {
	if peer == nil {
		return nil
	}
	owner.mu.Lock()
	if owner.peers == nil {
		owner.peers = make(map[*nativeStartupPeerV1]*nativePeerRetirementV1)
	}
	record := owner.peers[peer]
	if record == nil {
		record = &nativePeerRetirementV1{peer: peer, unknown: placementError("the original peer close has not completed its same-object native operation")}
		owner.peers[peer] = record
	}
	owner.mu.Unlock()
	peer.once.Do(func() {
		peer.mu.Lock()
		defer peer.mu.Unlock()
		record.mu.Lock()
		defer record.mu.Unlock()
		record.pidfd, record.proc, record.identity = peer.pidfd, peer.proc, peer.identity
		if peer.closed || peer.pidfd < 0 {
			record.unknown = placementError("the original native peer lost its first-close process handle")
			return
		}
		peer.closed = true
		// This wrapper is retained before its one Close. It owns the SAME
		// original integer descriptor from this genuine peer, never a retry.
		record.handle = os.NewFile(uintptr(peer.pidfd), "actual-original-purge-peer-retirement")
		fileErr := owner.closeFilesV4([]*os.File{record.handle})
		identityErr := owner.closeIdentityV4(record.identity)
		rootErr := owner.closeRootsV4([]*os.Root{record.proc})
		record.unknown = nil
		peer.closeErr = errors.Join(fileErr, identityErr, rootErr)
	})
	return errors.Join(peer.closeErr, owner.checkPeerV4(peer))
}

func (owner *nativeStartupRetirementsV1) checkPeerV4(peer *nativeStartupPeerV1) error {
	if peer == nil {
		return nil
	}
	owner.mu.Lock()
	record := owner.peers[peer]
	owner.mu.Unlock()
	if record == nil {
		return placementError("the original peer has no retained first native resource-close proof")
	}
	record.mu.Lock()
	bound, unknown, handle, process, identity := record.peer == peer && record.pidfd == peer.pidfd && record.proc == peer.proc && record.identity == peer.identity, record.unknown, record.handle, record.proc, record.identity
	record.mu.Unlock()
	if !bound || unknown != nil || handle == nil {
		return errors.Join(placementError("the original peer has no retained first native resource-close proof"), unknown)
	}
	return errors.Join(owner.checkFilesV4([]*os.File{handle}), owner.checkRootsV4([]*os.Root{process}), owner.checkIdentityV4(identity))
}

func (owner *nativeStartupRetirementsV1) closeSessionLeaseV4(lease *sessionFileLease) error {
	if lease == nil {
		return nil
	}
	owner.mu.Lock()
	if owner.leases == nil {
		owner.leases = make(map[*sessionFileLease]*nativeLeaseRetirementV1)
	}
	record := owner.leases[lease]
	if record == nil {
		record = &nativeLeaseRetirementV1{lease: lease, unknown: placementError("the original lease has no completed first native close")}
		owner.leases[lease] = record
	}
	owner.mu.Unlock()
	lease.once.Do(func() {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		record.mu.Lock()
		defer record.mu.Unlock()
		record.file, record.root = lease.file, lease.root
		if lease.closed {
			return
		}
		lease.closed = true
		lease.closeErr = errors.Join(owner.closeFilesV4([]*os.File{record.file}), owner.closeRootsV4([]*os.Root{record.root}))
		record.unknown = nil
	})
	return errors.Join(lease.closeErr, owner.checkSessionLeaseV4(lease))
}

func (owner *nativeStartupRetirementsV1) checkSessionLeaseV4(lease *sessionFileLease) error {
	if lease == nil {
		return nil
	}
	owner.mu.Lock()
	record := owner.leases[lease]
	owner.mu.Unlock()
	if record == nil {
		return placementError("the original lease has no retained same-object native retirement")
	}
	record.mu.Lock()
	bound, unknown, file, root := record.lease == lease && record.file == lease.file && record.root == lease.root, record.unknown, record.file, record.root
	record.mu.Unlock()
	if !bound || unknown != nil {
		return errors.Join(placementError("the original lease has no retained same-object native retirement"), unknown)
	}
	return errors.Join(owner.checkFilesV4([]*os.File{file}), owner.checkRootsV4([]*os.Root{root}))
}
