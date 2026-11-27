package jail

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

type gvisorControllerNativeV1 struct {
	creation, creatorCreation, task, creator *os.File
	birth                                    gvisorTaskV2
	pid                                      int
}

// Close drops temporary comparison handles while the original Cmd retains
// every atomic creation and actual native lifetime source.
func (native *gvisorControllerNativeV1) Close() error {
	if native == nil {
		return nil
	}
	var result error
	for _, file := range []*os.File{native.creation, native.creatorCreation, native.task, native.creator} {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}
func (owner *gvisorControllerOwnerV1) currentNativeV1() (_ *gvisorControllerNativeV1, failureErr error) {
	command := owner.command
	if command == nil || owner.original != owner {
		return nil, gvisorHandoffFailureV2("the controller has no original command owner")
	}
	command.mu.Lock()
	outer := command.gvisorOuter
	if outer == nil || outer.controller != owner || !command.owned || command.pidfd < 0 || command.reaped || command.physical || command.groupUnsafe || command.phase != phaseRunning && command.phase != phaseWaiting {
		command.mu.Unlock()
		return nil, gvisorHandoffFailureV2("the original controller command ended or became uncertain")
	}
	var originalSetup setup
	if err := json.Unmarshal(command.setup, &originalSetup); err != nil || originalSetup.ControllerPublicV1 == nil || *originalSetup.ControllerPublicV1 != owner.public {
		command.mu.Unlock()
		return nil, errors.Join(err, gvisorHandoffFailureV2("the original controller public projection changed before native release"))
	}
	if err := checkNativeLivePIDFDV1(command.pidfd); err != nil {
		command.mu.Unlock()
		return nil, err
	}
	nested := outer.nested
	creator, err := retainGvisorOriginalV1(outer.child)
	command.mu.Unlock()
	if err != nil {
		return nil, err
	}
	native := &gvisorControllerNativeV1{creatorCreation: creator}
	complete := false
	defer func() {
		if !complete {
			failureErr = errors.Join(failureErr, native.Close())
		}
	}()
	if nested == nil {
		return nil, gvisorHandoffFailureV2("the original C creation report is missing")
	}
	nested.mu.Lock()
	err = nested.checkLockedV1()
	if err == nil {
		native.creation, err = retainGvisorOriginalV1(nested.creation)
	}
	original := nested.birth
	var creatorBirth gvisorTaskV2
	if err == nil {
		creatorBirth, _, err = nativeGvisorTaskV1(nested.creator)
	}
	nested.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, file := range []*os.File{native.creation, native.creatorCreation} {
		if err := checkNativeLivePIDFDV1(int(file.Fd())); err != nil {
			return nil, err
		}
	}
	pids, err := gvisorTransferredPIDsV1(native.creation)
	if err != nil || len(pids) != 3 || pids[1] != original.pid {
		return nil, errors.Join(err, gvisorHandoffFailureV2("the current C creation pidfd changed native PID ancestry"))
	}
	parents, err := gvisorTransferredPIDsV1(native.creatorCreation)
	if err != nil || len(parents) != 2 {
		return nil, errors.Join(err, gvisorHandoffFailureV2("the original A creation pidfd changed native PID ancestry"))
	}
	native.pid = pids[0]
	native.task, err = openGvisorProcV2("/proc/" + strconv.Itoa(native.pid))
	if err != nil {
		return nil, err
	}
	native.birth, _, err = nativeGvisorTaskV1(native.task)
	if err != nil || native.birth.pid != native.pid || native.birth.parent != parents[0] || native.birth.birth != original.birth {
		return nil, errors.Join(err, gvisorHandoffFailureV2("current original C task birth differs from its atomic creation"))
	}
	native.creator, err = openGvisorProcV2("/proc/" + strconv.Itoa(parents[0]))
	if err != nil {
		return nil, err
	}
	actualCreator, _, err := nativeGvisorTaskV1(native.creator)
	if err != nil || actualCreator.pid != parents[0] || actualCreator.birth != creatorBirth.birth || creatorBirth.pid != 1 || creatorBirth.parent != 0 {
		return nil, errors.Join(err, gvisorHandoffFailureV2("the current native A differs from the original C creator"))
	}
	if err := checkGvisorCurrentCodeV1(native.creator, outer.helper); err != nil {
		return nil, err
	}
	complete = true
	return native, nil
}

func sameGvisorNativeObjectV1(left, right *os.File) error {
	if left == nil || right == nil {
		return gvisorHandoffFailureV2("an original native object is missing")
	}
	a, aErr := left.Stat()
	b, bErr := right.Stat()
	if aErr != nil || bErr != nil || !os.SameFile(a, b) {
		return errors.Join(aErr, bErr, gvisorHandoffFailureV2("a native object differs from its original held source"))
	}
	return nil
}
func openGvisorActorObjectV1(task *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(task.Fd()), name, flags|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "current-original-controller-native-object"), nil
}
func checkGvisorCurrentCodeV1(task, code *os.File) error {
	actual, err := openGvisorActorObjectV1(task, "exe", unix.O_PATH)
	if err != nil {
		return err
	}
	return errors.Join(sameGvisorNativeObjectV1(actual, code), actual.Close())
}
func checkGvisorNSKindV1(file *os.File, expected int) error {
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil || fs.Type != 0x6e736673 {
		return errors.Join(err, gvisorHandoffFailureV2("controller namespace is not an original NSFS object"))
	}
	kind, _, errno := unix.RawSyscall(unix.SYS_IOCTL, file.Fd(), unix.NS_GET_NSTYPE, 0)
	if errno != 0 || int(kind) != expected {
		return errors.Join(errno, gvisorHandoffFailureV2("controller namespace changed its native kind"))
	}
	return nil
}
func checkGvisorNSLinkV1(file *os.File, request int, expected *os.File) error {
	fd, _, errno := unix.RawSyscall(unix.SYS_IOCTL, file.Fd(), uintptr(request), 0)
	if errno != 0 {
		return errno
	}
	actual := os.NewFile(fd, "actual-original-controller-namespace-relation")
	return errors.Join(sameGvisorNativeObjectV1(actual, expected), actual.Close())
}

// Every namespace comes from independent CURRENT anchored task opens. These
// strict observations never fall back to fields after EACCES. If this host
// cannot make them, it retains the original C and refuses all private input.
func checkGvisorControllerNamespacesV1(native *gvisorControllerNativeV1, ownTask *os.File, received []*os.File) error {
	if native == nil || ownTask == nil || len(received) != 6 {
		return gvisorHandoffFailureV2("the original controller namespace vector is incomplete")
	}
	names := []string{"user", "pid", "mnt", "ipc", "net", "cgroup"}
	kinds := []int{unix.CLONE_NEWUSER, unix.CLONE_NEWPID, unix.CLONE_NEWNS, unix.CLONE_NEWIPC, unix.CLONE_NEWNET, unix.CLONE_NEWCGROUP}
	var parent []*os.File
	defer func() { closeFiles(parent) }()
	for index, name := range names {
		actual, err := openGvisorActorObjectV1(native.task, "ns/"+name, unix.O_RDONLY)
		if err != nil {
			return err
		}
		err = errors.Join(sameGvisorNativeObjectV1(actual, received[index]), actual.Close())
		if err != nil {
			return err
		}
		own, err := openGvisorActorObjectV1(ownTask, "ns/"+name, unix.O_RDONLY)
		if err != nil {
			return err
		}
		err = errors.Join(sameGvisorNativeObjectV1(own, received[index]), own.Close())
		if err != nil {
			return err
		}
		if err := checkGvisorNSKindV1(received[index], kinds[index]); err != nil {
			return err
		}
		originalParent, err := openGvisorActorObjectV1(native.creator, "ns/"+name, unix.O_RDONLY)
		if err != nil {
			return err
		}
		parent = append(parent, originalParent)
		if err := checkGvisorNSKindV1(originalParent, kinds[index]); err != nil {
			return err
		}
	}
	// C self-maps a fresh ONE-ID user namespace, and inherits A's empty net.
	if err := checkGvisorNSLinkV1(received[0], unix.NS_GET_PARENT, parent[0]); err != nil {
		return err
	}
	ownerUID, err := unix.IoctlGetInt(int(received[0].Fd()), unix.NS_GET_OWNER_UID)
	if err != nil || uint32(ownerUID) != uint32(os.Geteuid()) {
		return errors.Join(err, gvisorHandoffFailureV2("the original C user namespace has another actual creator UID"))
	}
	for index := 1; index < len(received); index++ {
		if index == 4 {
			if err := sameGvisorNativeObjectV1(received[index], parent[index]); err != nil {
				return err
			}
			if err := checkGvisorNSLinkV1(received[index], unix.NS_GET_USERNS, parent[0]); err != nil {
				return err
			}
			continue
		}
		if err := checkGvisorNSLinkV1(received[index], unix.NS_GET_USERNS, received[0]); err != nil {
			return err
		}
		if index == 1 {
			if err := checkGvisorNSLinkV1(received[index], unix.NS_GET_PARENT, parent[index]); err != nil {
				return err
			}
		}
		a, err := received[index].Stat()
		b, bErr := parent[index].Stat()
		if err != nil || bErr != nil || os.SameFile(a, b) {
			return errors.Join(err, bErr, gvisorHandoffFailureV2("the original C did not create its fresh closed namespace"))
		}
	}
	return nil
}

func checkGvisorControllerCurrentArgvV1(task *os.File) error {
	raw, err := readGvisorAtV2(task, "cmdline")
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, []byte("native-controller-setup-v1\x00native-controller-setup-v1\x00")) {
		return gvisorHandoffFailureV2("the actual original controller executes another native command vector")
	}
	return nil
}
