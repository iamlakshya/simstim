package jail

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

type gvisorOuterOwnerV1 struct {
	controller        *gvisorControllerOwnerV1
	fork              *gvisorForkRootOwnerV2
	launcher, helper  *os.File
	child             *os.File
	childFD           int
	noChild           bool
	bootstrapIdentity string
	nested            *gvisorNestedCreationV1
	codeClose         sync.Once
	codeCloseErr      error
}

func configureGvisorOuterCapturedV1(command *Cmd, source *GvisorBootstrapV1) error {
	launcher, helper, identity, err := source.retain(command.ctx)
	if err != nil {
		return err
	}
	command.gvisorOuter = &gvisorOuterOwnerV1{launcher: launcher, helper: helper, childFD: -1, bootstrapIdentity: identity}
	fork, err := captureGvisorForkBindingsV2(command)
	if err != nil {
		return errors.Join(err, command.gvisorOuter.closeCodeV1())
	}
	command.gvisorOuter.fork = fork
	command.cmd.Path = "/proc/self/fd/" + strconv.Itoa(int(launcher.Fd()))
	command.cmd.Args = []string{"fixed-native-launcher", "gvisor-outer-v1"}
	command.cmd.Dir = "/"
	command.cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"}
	command.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: unix.SIGKILL}
	return nil
}

func receiveGvisorNativeFrameV1(conn *net.UnixConn, peer unix.Ucred) (proto.Frame, []*os.File, error) {
	reader := &gvisorReaderV2{conn: conn, peer: peer}
	frame, err := proto.ReadFrom(reader)
	if err != nil {
		closeFiles(reader.files)
		return frame, nil, err
	}
	if frame.Type != proto.TResponse {
		closeFiles(reader.files)
		return frame, nil, gvisorHandoffFailureV2("the original native creator returned another frame role")
	}
	return frame, reader.files, nil
}

// The pidfd's kernel fdinfo binds the transferred original descriptor to its
// actual task. A serialized PID alone never issues a grant. NSpid must show
// the new namespace's PID1, and the original creator must remain unreaped.
func gvisorTransferredPIDV1(file *os.File) (int, error) {
	pids, err := gvisorTransferredPIDsV1(file)
	if err != nil {
		return 0, err
	}
	return pids[0], nil
}
func gvisorTransferredPIDsV1(file *os.File) ([]int, error) {
	if file == nil {
		return nil, gvisorHandoffFailureV2("the original creation pidfd is absent")
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0); err != nil {
		return nil, err
	}
	if err := unix.PidfdSendSignal(int(file.Fd()), 0, nil, 0); err != nil {
		return nil, err
	}
	data, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(int(file.Fd())))
	if err != nil {
		return nil, err
	}
	return parseGvisorPIDFDInfoV1(data)
}
func parseGvisorPIDFDInfoV1(data []byte) ([]int, error) {
	if len(data) > 8192 {
		return nil, gvisorHandoffFailureV2("the original creation fdinfo exceeds its native bound")
	}
	pid := 0
	var namespacePID []string
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "Pid":
			fields := strings.Fields(value)
			if len(fields) != 1 || pid != 0 {
				return nil, gvisorHandoffFailureV2("the transferred pidfd has an ambiguous native PID")
			}
			var err error
			pid, err = strconv.Atoi(fields[0])
			if err != nil || pid <= 0 || strconv.Itoa(pid) != fields[0] {
				return nil, gvisorHandoffFailureV2("the transferred pidfd has no actual live task")
			}
		case "NSpid":
			if namespacePID != nil {
				return nil, gvisorHandoffFailureV2("the transferred pidfd has ambiguous PID ancestry")
			}
			namespacePID = strings.Fields(value)
			if len(namespacePID) == 0 {
				return nil, gvisorHandoffFailureV2("the transferred pidfd has no native PID ancestry")
			}
		}
	}
	if pid <= 0 || len(namespacePID) < 2 || namespacePID[0] != strconv.Itoa(pid) || namespacePID[len(namespacePID)-1] != "1" {
		return nil, gvisorHandoffFailureV2("the actual transferred task is not the new PID namespace's PID1")
	}
	pids := make([]int, len(namespacePID))
	for index, field := range namespacePID {
		value, err := strconv.Atoi(field)
		if err != nil || value <= 0 || strconv.Itoa(value) != field {
			return nil, gvisorHandoffFailureV2("the original creation pidfd has malformed native PID ancestry")
		}
		pids[index] = value
	}
	return pids, nil
}

func (c *Cmd) startGvisorOuterV1(files []*os.File, release func()) (failureErr error) {
	owner := c.gvisorOuter
	identity, codeErr := gvisorCodeClosureV1(c.ctx, owner.launcher, owner.helper)
	if codeErr != nil || identity != owner.bootstrapIdentity {
		return c.failGvisorBeforeStartV1(errors.Join(codeErr, gvisorHandoffFailureV2("the original held bootstrap changed before native creation")))
	}
	slotCount := 5 + len(files) + len(c.pins)
	if slotCount < 6 || slotCount > maxGvisorSlotsV2 {
		return c.failGvisorBeforeStartV1(gvisorHandoffFailureV2("the actual original descriptor layout exceeds its closed bound"))
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return c.failGvisorBeforeStartV1(err)
	}
	hostFile := os.NewFile(uintptr(pair[0]), "original-gvisor-native-creator")
	childFile := os.NewFile(uintptr(pair[1]), "original-gvisor-native-control")
	defer childFile.Close()
	value, err := net.FileConn(hostFile)
	closeErr := hostFile.Close()
	if err != nil || closeErr != nil {
		return c.failGvisorBeforeStartV1(errors.Join(err, closeErr))
	}
	bootstrap := value.(*net.UnixConn)
	defer bootstrap.Close()
	if err = proto.EnableUnixCredentialsV1(bootstrap); err != nil {
		return c.failGvisorBeforeStartV1(err)
	}
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return c.failGvisorBeforeStartV1(err)
	}
	defer null.Close()
	streams := make([]*os.File, 3)
	for i, stream := range []any{c.cmd.Stdin, c.cmd.Stdout, c.cmd.Stderr} {
		if stream == nil {
			streams[i] = null
		} else {
			streams[i] = stream.(*os.File)
		}
	}
	c.cmd.Stdin, c.cmd.Stdout, c.cmd.Stderr = null, null, null
	c.cmd.ExtraFiles = []*os.File{childFile, owner.helper}
	creatorFD := -1
	c.cmd.SysProcAttr.PidFD = &creatorFD
	if err = c.prepareGvisorForkFreezerV1(); err != nil {
		return c.failGvisorBeforeStartV1(err)
	}
	if err = c.cmd.Start(); err != nil {
		return c.failGvisorBeforeStartV1(err)
	}
	c.mu.Lock()
	c.pid, c.pidfd, c.owned = c.cmd.Process.Pid, creatorFD, true
	c.mu.Unlock()
	_ = childFile.Close()
	if creatorFD < 0 {
		return c.failGvisorAfterStartV1(gvisorHandoffFailureV2("the actual C creator has no creation pidfd"))
	}
	if err = c.bindGvisorForkCreatorV1(creatorFD, c.cmd.Process.Pid); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	if err = bootstrap.SetDeadline(deadline); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	creator := unix.Ucred{Pid: int32(c.pid), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}
	first, rights, err := receiveGvisorNativeFrameV1(bootstrap, creator)
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	if len(first.Payload) == 5 && first.Payload[0] == 'F' && len(rights) == 0 && binary.LittleEndian.Uint32(first.Payload[1:]) != 0 {
		owner.noChild = true
		return c.failGvisorAfterStartV1(gvisorHandoffFailureV2("the original native creator could not create its new outer namespace"))
	}
	if len(first.Payload) != 5 || first.Payload[0] != 'P' || len(rights) != 1 {
		closeFiles(rights)
		return c.failGvisorAfterStartV1(gvisorHandoffFailureV2("the original creator omitted its exact actual A creation descriptor"))
	}
	actualPID, err := gvisorTransferredPIDV1(rights[0])
	if err != nil || uint32(actualPID) != binary.LittleEndian.Uint32(first.Payload[1:]) {
		closeFiles(rights)
		return c.failGvisorAfterStartV1(errors.Join(err, gvisorHandoffFailureV2("the creator's task number differs from its actual pidfd")))
	}
	owner.childFD = int(rights[0].Fd())
	// Ownership moves to this exact wrapper until both original native tasks
	// have ended; it is never reconstructed from an advertised/saved PID.
	owner.child = rights[0]
	task, err := openGvisorProcV2("/proc/" + strconv.Itoa(actualPID))
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	defer task.Close()
	creatorProc, err := openGvisorProcV2("/proc/" + strconv.Itoa(c.pid))
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	defer creatorProc.Close()
	outside, err := openGvisorProcV2("/proc/self")
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	defer outside.Close()
	initialRaw, err := readGvisorAtV2(task, "stat")
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	initial, err := parseGvisorTaskV2(initialRaw)
	if err != nil || initial.pid != actualPID || initial.parent != c.pid {
		return c.failGvisorAfterStartV1(errors.Join(err, gvisorHandoffFailureV2("the actual namespace child has another original creator")))
	}
	activation := make([]byte, 5)
	activation[0] = 'G'
	binary.LittleEndian.PutUint32(activation[1:], uint32(slotCount))
	if err = proto.SendUnixFrameV1(bootstrap, proto.TRequest, activation, nil); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	childPeer := unix.Ucred{Pid: int32(actualPID), Uid: creator.Uid, Gid: creator.Gid}
	next, newRights, err := receiveGvisorNativeFrameV1(bootstrap, childPeer)
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	if len(next.Payload) != 1 || next.Payload[0] != 'N' || len(newRights) != 1 {
		closeFiles(newRights)
		return c.failGvisorAfterStartV1(gvisorHandoffFailureV2("the actual mapped child omitted its fresh post-map control"))
	}
	connectionValue, err := net.FileConn(newRights[0])
	closeErr = newRights[0].Close()
	if err != nil || closeErr != nil {
		return c.failGvisorAfterStartV1(errors.Join(err, closeErr))
	}
	connection, ok := connectionValue.(*net.UnixConn)
	if !ok {
		_ = connectionValue.Close()
		return c.failGvisorAfterStartV1(gvisorHandoffFailureV2("the fresh mapped control is not Unix"))
	}
	defer connection.Close()
	if err = errors.Join(connection.SetDeadline(deadline), proto.EnableUnixCredentialsV1(connection)); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	peer, err := gvisorPeerV2(connection)
	if err != nil || peer != childPeer {
		return c.failGvisorAfterStartV1(errors.Join(err, gvisorHandoffFailureV2("the fresh control's actual creator differs from original A")))
	}
	mapped, helperRights, err := receiveGvisorV2(connection, childPeer, "outer-mapped", 1)
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	if mapped.SlotCount != slotCount {
		closeFiles(helperRights)
		return c.failGvisorAfterStartV1(gvisorHandoffFailureV2("the native mapped slot layout differs from the actual original Cmd"))
	}
	expected, expectedErr := owner.helper.Stat()
	actual, actualErr := helperRights[0].Stat()
	checkErr := errors.Join(expectedErr, actualErr, helperRights[0].Close())
	if checkErr != nil || !os.SameFile(expected, actual) {
		return c.failGvisorAfterStartV1(errors.Join(checkErr, gvisorHandoffFailureV2("the actual mapped helper executable differs from the original fixed code")))
	}
	if err = sendGvisorV2(connection, gvisorMessageV2{Phase: "outer-ancestry", SocketFD: mapped.SocketFD}, []*os.File{task, creatorProc, outside}); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	if _, _, err = receiveGvisorV2(connection, childPeer, "outer-proved", 0); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	c.status = statusR
	defer statusW.Close()
	var set setup
	if err = json.Unmarshal(c.setup, &set); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	slots := []int{0, 1, 2}
	sources := append([]*os.File(nil), streams...)
	for i, file := range files {
		slots = append(slots, 3+i)
		sources = append(sources, file)
	}
	slots = append(slots, set.StatusFD)
	sources = append(sources, statusW)
	for i, file := range c.pins {
		slots = append(slots, 5+len(files)+i)
		sources = append(sources, file)
	}
	if len(slots) > maxGvisorSlotsV2 {
		return c.failGvisorAfterStartV1(gvisorHandoffFailureV2("the original outer vector exceeds its fixed bound"))
	}
	if err = sendGvisorV2(connection, gvisorMessageV2{Phase: "plan", Setup: &set, Slots: slots}, nil); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	for offset := 0; offset < len(sources); offset += 8 {
		end := min(offset+8, len(sources))
		if err = sendGvisorV2(connection, gvisorMessageV2{Phase: "files", Slots: slots[offset:end]}, sources[offset:end]); err != nil {
			return c.failGvisorAfterStartV1(err)
		}
	}
	release()
	c.closePins()
	c.closeEnds(false)
	if err = statusW.Close(); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	finalRaw, err := readGvisorAtV2(task, "stat")
	if err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	final, err := parseGvisorTaskV2(finalRaw)
	if err != nil || final != initial {
		return c.failGvisorAfterStartV1(errors.Join(err, gvisorHandoffFailureV2("the actual A native task changed before private setup release")))
	}
	if err := owner.controller.releaseSetupAliasV1(); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	// Allocate the pending lifetime before A may create C. An omitted or
	// malformed report leaves this owner incomplete; A exit or EOF cannot
	// turn it into evidence that no nested child was created.
	c.mu.Lock()
	owner.nested = &gvisorNestedCreationV1{outer: owner}
	c.mu.Unlock()
	if err = sendGvisorV2(connection, gvisorMessageV2{Phase: "release"}, nil); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	if err = c.receiveGvisorNestedCreationV1(connection, childPeer, task, initial); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	if err = c.toRunning(); err != nil {
		return c.failGvisorAfterStartV1(err)
	}
	go c.watch()
	return nil
}

func (c *Cmd) failGvisorBeforeStartV1(err error) error {
	c.closeEnds(true)
	c.mu.Lock()
	c.phase, c.physical = phaseFailed, true
	c.lifecycleErr = errors.Join(c.lifecycleErr, err)
	c.mu.Unlock()
	return err
}

func (c *Cmd) failGvisorAfterStartV1(err error) error {
	// Signal only original retained native descriptors. Join BOTH tasks before
	// Start's captured private files/runtime scope can be released by its caller.
	c.mu.Lock()
	c.lifecycleErr = errors.Join(c.lifecycleErr, err)
	c.mu.Unlock()
	_ = c.killGroup()
	for {
		if c.gvisorOuter.childFD >= 0 {
			if ready, _ := ownedPIDFDReady(c.gvisorOuter.childFD, 100); ready {
				break
			}
		} else if c.gvisorOuter.noChild {
			break
		}
		// No P/F means unknown, including a crash in clone-to-transfer. Creator
		// EOF or exit must not be invented as actual A death. Retain this owner.
		time.Sleep(100 * time.Millisecond)
	}
	for {
		if ready, _ := ownedPIDFDReady(c.pidfd, 100); ready {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.mu.Lock()
	nested := c.gvisorOuter.nested
	c.mu.Unlock()
	if nested != nil && nested.checkV1() != nil {
		return c.uncertainWait(errors.Join(err, gvisorHandoffFailureV2("the original nested C creation report remains unknown")))
	}
	result := c.completeOwnedWait(false)
	c.mu.Lock()
	c.phase = phaseFailed
	c.mu.Unlock()
	return errors.Join(err, result)
}

func (owner *gvisorOuterOwnerV1) joinActualA(nested *gvisorNestedCreationV1) error {
	if owner.noChild {
		return nil
	}
	if owner.childFD < 0 {
		return gvisorHandoffFailureV2("the original A physical lifetime remains unknown")
	}
	if err := waitOwnedPIDFD(owner.childFD, -1); err != nil {
		return err
	}
	if nested != nil {
		return nested.joinV1()
	}
	return nil
}

func (owner *gvisorOuterOwnerV1) closeCodeV1() error {
	// Fork root borrows join without Cmd.mu. A borrower checks this same
	// native Cmd, so joining under its mutex would invert the lifetime locks.
	if owner.fork != nil {
		if err := owner.fork.close(); err != nil {
			return err
		}
	}
	owner.codeClose.Do(func() {
		owner.codeCloseErr = errors.Join(owner.launcher.Close(), owner.helper.Close(), owner.controller.closeV1())
	})
	return owner.codeCloseErr
}
