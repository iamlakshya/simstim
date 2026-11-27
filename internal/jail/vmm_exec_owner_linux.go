//go:build linux

package jail

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// This owner is reachable only from the original concrete Cmd. Its native
// channel records real helper creations while each child is still blocked on
// its existing setup pipe. Neither a setup field nor a stage record issues it.
type vmmExecOwnerV1 struct {
	mu                                       sync.Mutex
	outerCreator                             *vmmOuterCreatorV1
	outerReceipt                             *vmmOuterExecReceiptV1
	creationThreadLocked                     bool
	hostClose                                sync.Once
	hostCloseErr                             error
	source                                   *supply.ExecModeLeaseV1
	program, code, lease                     *os.File
	reader, writer                           *os.File
	host                                     *net.UnixConn
	child                                    *os.File
	control                                  *net.UnixConn
	layout                                   vmmExecSetupV1
	root, init, runner, final                *vmmExecActorV1
	actors                                   []*vmmExecActorV1
	runnerReady, finalReady, nativeDone      chan struct{}
	nativeErr                                error
	recordMu                                 sync.Mutex
	noCreation, released                     bool
	nativeStarted, namespaceRootProved       bool
	runnerOnce                               sync.Once
	projection, armed, finalProved, noFuture bool
	rootNamespace                            bool
	readonly                                 bool
	readonlyPlan                             *VMMReadonlyInitPlanV1
	readonlyControlTerminal                  bool
	readonlyControlDone                      chan struct{}
	readonlyProjectionReady                  chan struct{}
	readonlyDiskSent                         bool
	readonlyParent                           *os.File // borrowed original prepared VM-directory anchor
	readonlyDiskParent                       *os.File // borrowed from SAME received phase owner
	readonlyDisk                             *os.File // borrowed from SAME phase owner
	readonlyDiskAdopted                      bool
	readonlyParentApproved                   bool
	readonlyGroup                            *readonlyVMMGroupV1
	readonlyPhysicalClosed                   bool
	readonlyCodeCloseAttempted               bool
	readonlyCodeClose                        supply.FileCloseFactsV1
	readonlyContinued                        bool
	subUID, subGID                           uint32 // actual execution IDs, independent of mapping admission
	readonlyLoginUID                         bool
	readonlyMapSubGID                        uint32
}

type vmmExecActorV1 struct {
	process                   *os.File // SAME actual creation pidfd, never pidfd_open
	task                      *os.File // anchored native proc object, correlated while pidfd is live
	pid                       int
	birth                     uint64
	stage                     byte
	entered, issued, terminal bool
	namespaces                [3]os.FileInfo
}

// RetainVMMExecLeaseV1 binds an opaque store lease to this exact unstarted VMM
// Cmd, its original program, fixed helper code and complete nine-slot purpose.
func (c *Cmd) RetainVMMExecLeaseV1(source *supply.ExecModeLeaseV1) error {
	if c == nil || source == nil {
		return vmmModeFailureV1("the original VMM Cmd or mode source is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != phaseNew || c.modeExecV1 != nil || c.publicationLeaseV1 != nil || c.gvisorOuter != nil || c.slotSourceV1 != nil || len(c.files) != 9 || c.cmd == nil || c.cmd.Process != nil || c.cmd.Path != self {
		return vmmModeFailureV1("the mode source cannot attach to this command purpose or lifetime")
	}
	var set setup
	if err := json.Unmarshal(c.setup, &set); err != nil {
		return err
	}
	if !set.Userns || set.Net != NetNone || set.Seccomp != ProfileVMM || set.ExecFD != 8 || len(set.Argv) != 2 || set.Argv[0] != "/proc/self/fd/8" || set.Argv[1] != "chv-run" || set.Files != 9 || set.VMMExecV1 != nil || set.PublicationLeaseV1 != nil || set.ControllerPublicV1 != nil {
		return vmmModeFailureV1("the mode source needs the original closed namespace VMM runner")
	}
	if err := source.CheckProgramV1(c.ctx, c.files[0]); err != nil {
		return err
	}
	code, err := os.Open(self)
	if err != nil {
		return err
	}
	expected, err := code.Stat()
	actual, actualErr := c.files[5].Stat()
	if err != nil || actualErr != nil || !os.SameFile(expected, actual) {
		code.Close()
		return errors.Join(err, actualErr, vmmModeFailureV1("the VMM runner has another actual helper executable"))
	}
	readonly, err := source.ReadonlyOriginalV1(c.ctx)
	if err != nil {
		code.Close()
		return err
	}
	if readonly && set.Sub == nil {
		// Only this actual opaque original source may admit the distinct
		// login-UID/two-GID route. A Spec or UIDMode cannot select a map.
		mapped, err := lookupReadonlyLoginMapV1()
		if err != nil {
			code.Close()
			return err
		}
		set.Sub = &mapped
	}

	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		code.Close()
		return err
	}
	hostFile := os.NewFile(uintptr(pair[0]), "original-vmm-creation-host")
	child := os.NewFile(uintptr(pair[1]), "original-vmm-creation-child")
	value, err := net.FileConn(hostFile)
	closeErr := hostFile.Close()
	if err != nil || closeErr != nil {
		code.Close()
		child.Close()
		if value != nil {
			value.Close()
		}
		return errors.Join(err, closeErr)
	}
	host, ok := value.(*net.UnixConn)
	if !ok {
		value.Close()
		code.Close()
		child.Close()
		return vmmModeFailureV1("the original creation channel is not Unix")
	}
	if err := proto.EnableUnixCredentialsV1(host); err != nil {
		host.Close()
		child.Close()
		code.Close()
		return err
	}
	owner := &vmmExecOwnerV1{source: source, program: c.files[0], code: code, host: host, child: child, runnerReady: make(chan struct{}), finalReady: make(chan struct{}), nativeDone: make(chan struct{}), rootNamespace: set.Sub == nil, readonly: readonly, readonlyControlDone: make(chan struct{}), readonlyProjectionReady: make(chan struct{})}
	owner.subUID, owner.subGID = uint32(os.Getuid()), uint32(os.Getgid())
	if set.Sub != nil {
		if readonly && set.Sub.SubUID == -1 {
			owner.readonlyLoginUID = true
			owner.readonlyMapSubGID = uint32(set.Sub.SubGID)
		} else {
			owner.subUID, owner.subGID = uint32(set.Sub.SubUID), uint32(set.Sub.SubGID)
		}
		if readonly && !owner.readonlyLoginUID {
			owner.readonlyMapSubGID = owner.subGID
		}
	}
	owner.layout = vmmExecSetupV1{ReadonlyInit: readonly, Version: 2, LeaseFD: 5 + len(c.files) + len(c.pins), WriterFD: 6 + len(c.files) + len(c.pins), ControlFD: 7 + len(c.files) + len(c.pins)}
	if readonly {
		owner.readonlyParent = c.files[8]
		var parent unix.Stat_t
		if err := unix.Fstat(int(owner.readonlyParent.Fd()), &parent); err != nil || len(c.binds) == 0 || c.binds[0].Kind != kindDir || !c.binds[0].Write || uint64(parent.Dev) != c.binds[0].Dev || uint64(parent.Ino) != c.binds[0].Ino {
			host.Close()
			child.Close()
			code.Close()
			return errors.Join(err, vmmModeFailureV1("the original read-only parent differs from the actual pinned VM bind"))
		}
		if err := owner.checkReadonlyParentV2(owner.readonlyParent); err != nil {
			host.Close()
			child.Close()
			code.Close()
			return err
		}
		nonce, err := source.ReadonlyNonceV1(c.ctx)
		if err != nil {
			host.Close()
			child.Close()
			code.Close()
			return err
		}
		owner.layout.ReadonlyNonce = hex.EncodeToString(nonce[:])
	}
	set.VMMExecV1 = &owner.layout
	encoded, err := json.Marshal(set)
	if err != nil {
		host.Close()
		child.Close()
		code.Close()
		return err
	}
	// Borrow the actual source LAST. No later allocation/marshal error can
	// orphan its one original loan count before the Cmd owns the lender.
	lease, err := source.RetainForJailV1(c.ctx)
	if err != nil {
		host.Close()
		child.Close()
		code.Close()
		return err
	}
	owner.lease = lease
	c.setup, c.modeExecV1 = encoded, owner
	if owner.readonlyLoginUID {
		// The SAME actual Cmd now creates its original outside mapper A.
		// Its C still receives all original atomic creation/native checks.
		c.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		c.cmd.Args[1] = stageOuterVMMV1
	} else {
		c.cmd.Args[1] = vmmFileRulesStageV1(c.cmd.Args[1], &set)
	}
	return nil
}

// RetainVMMFinalBoundaryV1 retains exact opposite endpoints of the original
// anonymous pipe. The Cmd alone reads it; no ordinary control EOF is proof.
func (c *Cmd) RetainVMMFinalBoundaryV1(source *supply.ExecModeLeaseV1, reader, writer *os.File) error {
	if c == nil {
		return vmmModeFailureV1("the original VMM Cmd is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owner := c.modeExecV1
	if c.phase != phaseNew || owner == nil || owner.source != source || owner.reader != nil {
		return vmmModeFailureV1("the final boundary belongs to another mode source")
	}
	if err := checkVMMModePipeV1(reader, writer); err != nil {
		return err
	}
	r, err := captureFile(reader, "VMM final boundary reader", "Retain the original anonymous boundary pipe.")
	if err != nil {
		return err
	}
	w, err := captureFile(writer, "VMM final boundary writer", "Retain the original anonymous boundary pipe.")
	if err != nil {
		r.Close()
		return err
	}
	owner.reader, owner.writer = r, w
	return nil
}

// BindVMMExecControlV1 captures the original live control object before Start.
// Adoption still needs its unchanged kernel receive witness and creation chain.
func (c *Cmd) BindVMMExecControlV1(source *supply.ExecModeLeaseV1, conn *net.UnixConn) error {
	if c == nil || conn == nil {
		return vmmModeFailureV1("the original VMM control is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owner := c.modeExecV1
	if c.phase != phaseNew || owner == nil || owner.source != source || owner.control != nil || owner.reader == nil || owner.writer == nil {
		return vmmModeFailureV1("the VMM control arrived outside its original constructor")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(c.files[7].Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return vmmModeFailureV1("the original runner control slot is not a socket")
	}
	if err := checkVMMControlPairV1(conn, c.files[7]); err != nil {
		return err
	}
	owner.control = conn
	return nil
}

// vmmCapturedFilesV1 runs before the actual first creation. The caller appends
// these three exact files after its existing pins; generic Files never change.
func (c *Cmd) vmmCapturedFilesV1() ([]*os.File, error) {
	owner := c.modeExecV1
	if owner == nil {
		return nil, nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.control == nil || owner.reader == nil || owner.writer == nil || owner.child == nil {
		return nil, vmmModeFailureV1("the original VMM mode constructor is incomplete")
	}
	if err := owner.captureOuterCreatorV1(c); err != nil {
		return nil, err
	}
	if err := owner.source.CheckProgramV1(c.ctx, owner.program); err != nil {
		return nil, err
	}
	expected, err := owner.code.Stat()
	actual, other := os.Stat(self)
	if err != nil || other != nil || !os.SameFile(expected, actual) {
		return nil, errors.Join(err, other, vmmModeFailureV1("the fixed helper executable changed before creation"))
	}
	return []*os.File{owner.lease, owner.writer, owner.child}, nil
}

// vmmCreatedRootV1 consumes only the actual atomic creation result, while the
// original child is blocked on the not-yet-written setup pipe.
func (c *Cmd) vmmCreatedRootV1(createdFD int) error {
	owner := c.modeExecV1
	if owner == nil {
		return nil
	}
	defer c.releaseVMMCreationThreadV1()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.root != nil || createdFD < 0 {
		return vmmModeFailureV1("the original first creation has no unique native handle")
	}
	fd, err := unix.FcntlInt(uintptr(createdFD), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "original-vmm-root-creation")
	actor, err := captureVMMActorV1(file)
	if err != nil {
		file.Close()
		return err
	}
	c.mu.Lock()
	same := c.owned && c.pidfd == createdFD && c.pid == actor.pid
	c.mu.Unlock()
	if !same {
		actor.close()
		return vmmModeFailureV1("the atomic root handle differs from its original Cmd")
	}
	actor.stage = vmmStageInitV1
	if !owner.rootNamespace {
		actor.stage = vmmStageOuterV1
	}
	owner.root = actor
	owner.actors = append(owner.actors, actor)
	if !owner.rootNamespace {
		if err := owner.issueOuterExecReceiptV1(c, actor, createdFD); err != nil {
			return err
		}
	}
	if owner.rootNamespace {
		_, namespace, e := vmmPIDFDIdentityV1(actor.process)
		if e != nil || len(namespace) < 2 || namespace[len(namespace)-1] != 1 {
			return errors.Join(e, vmmModeFailureV1("the actual original creation is not the expected namespace PID1"))
		}
		owner.namespaceRootProved = true
	}
	err = owner.child.Close()
	owner.child = nil
	owner.nativeStarted = true
	go owner.readNativeV1()
	return err
}

func (c *Cmd) vmmOwnerV1(source *supply.ExecModeLeaseV1) (*vmmExecOwnerV1, error) {
	if c == nil || source == nil {
		return nil, vmmModeFailureV1("the original VMM source is missing")
	}
	c.mu.Lock()
	owner := c.modeExecV1
	c.mu.Unlock()
	if owner == nil || owner.source != source {
		return nil, vmmModeFailureV1("the VMM proof belongs to another original source")
	}
	return owner, nil
}

func vmmModeFailureV1(message string) error {
	return failure(message, "Keep the original VMM command and runtime owner; inspect the host with host doctor.")
}

func checkVMMModePipeV1(reader, writer *os.File) error {
	if reader == nil || writer == nil {
		return vmmModeFailureV1("the native final boundary pipe is missing")
	}
	var a, b unix.Stat_t
	for _, file := range []*os.File{reader, writer} {
		var fs unix.Statfs_t
		if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil {
			return err
		}
		if fs.Type != 0x50495045 {
			return vmmModeFailureV1("the final boundary is not an anonymous kernel pipe")
		}
	}
	if err := errors.Join(unix.Fstat(int(reader.Fd()), &a), unix.Fstat(int(writer.Fd()), &b)); err != nil {
		return err
	}
	if a.Dev != b.Dev || a.Ino != b.Ino || a.Mode&unix.S_IFMT != unix.S_IFIFO || b.Mode&unix.S_IFMT != unix.S_IFIFO {
		return vmmModeFailureV1("the final boundary endpoints name different native pipes")
	}
	for i, file := range []*os.File{reader, writer} {
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil {
			return err
		}
		want := unix.O_RDONLY
		if i == 1 {
			want = unix.O_WRONLY
		}
		if flags&unix.O_ACCMODE != want {
			return vmmModeFailureV1("the final boundary has another native direction")
		}
	}
	return nil
}

func (actor *vmmExecActorV1) close() error {
	if actor == nil {
		return nil
	}
	return errors.Join(actor.process.Close(), actor.task.Close())
}

func (owner *vmmExecOwnerV1) releaseV1() error {
	owner.mu.Lock()
	if owner.released {
		owner.mu.Unlock()
		return nil
	}
	owner.released = true
	keepLifetime := owner.readonly && owner.nativeStarted && !owner.noFuture
	files := []*os.File{owner.writer, owner.reader, owner.lease, owner.child}
	if !keepLifetime {
		files = append(files, owner.code)
		owner.code = nil
	}
	if owner.outerCreator != nil {
		for _, file := range owner.outerCreator.namespaces {
			files = append(files, file)
		}
	}
	owner.writer, owner.reader, owner.lease, owner.child = nil, nil, nil, nil
	host := owner.host
	started := owner.nativeStarted
	owner.mu.Unlock()
	var result error
	if host != nil && !keepLifetime {
		result = errors.Join(result, owner.closeHostV1())
	}
	if started && !keepLifetime {
		<-owner.nativeDone
	}
	if !started && owner.readonlyGroup != nil {
		result = errors.Join(result, owner.readonlyGroup.Close())
	}
	for _, file := range files {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}
func (owner *vmmExecOwnerV1) closeNativeActorsV1() error {
	owner.mu.Lock()
	actors := owner.actors
	owner.actors = nil
	owner.mu.Unlock()
	var result error
	for _, actor := range actors {
		result = errors.Join(result, actor.close())
	}
	return result
}

// Keep the actual descriptor's fdinfo path local. No PID pathname is ever
// used to recover a replacement process handle.
func vmmFDInfoPathV1(file *os.File) string {
	return "/proc/self/fdinfo/" + strconv.Itoa(int(file.Fd()))
}

func (owner *vmmExecOwnerV1) closeHostV1() error {
	owner.hostClose.Do(func() { owner.hostCloseErr = owner.host.Close() })
	return owner.hostCloseErr
}
