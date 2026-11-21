package jail

import (
	"bytes"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type gvisorControllerWitnessV1 struct {
	endpointDev, endpointIno   uint64
	owner                      *gvisorControllerOwnerV1
	endpoint, executable, task *os.File
	namespaces                 []*os.File
	final                      *gvisorControllerFinalV1
}
type gvisorControllerFinalV1 struct{ original *gvisorControllerWitnessV1 }

func (witness *gvisorControllerWitnessV1) closeV1() error {
	if witness == nil {
		return nil
	}
	var result error
	for _, file := range append([]*os.File{witness.endpoint, witness.executable, witness.task}, witness.namespaces...) {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}
func gvisorControllerPeerV1(pid int) unix.Ucred {
	return unix.Ucred{Pid: int32(pid), Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid())}
}
func gvisorControllerReadDeadlineV1(file *os.File) error {
	timeout := unix.NsecToTimeval((10 * time.Second).Nanoseconds())
	return unix.SetsockoptTimeval(int(file.Fd()), unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout)
}

// Collect EVERY delivered right before semantic refusal, including malformed
// ancillary ordering. A bad packet cannot leak an untracked native object.
func receiveGvisorControllerPacketV1(file *os.File, expected []byte, count int, peer unix.Ucred, stream bool) (_ []*os.File, failureErr error) {
	data := make([]byte, len(expected)+1)
	ancillary := make([]byte, unix.CmsgSpace((count+1)*4)+unix.CmsgSpace(unix.SizeofUcred))
	flags := unix.MSG_CMSG_CLOEXEC
	if stream {
		flags |= unix.MSG_WAITALL
	}
	n, oobn, receivedFlags, _, err := unix.Recvmsg(int(file.Fd()), data, ancillary, flags)
	if err != nil {
		return nil, err
	}
	messages, parseErr := unix.ParseSocketControlMessage(ancillary[:oobn])
	var files []*os.File
	var sender *unix.Ucred
	credentials, rightFrames := 0, 0
	var semanticErr error
	for _, message := range messages {
		if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
			rightFrames++
			rights, err := unix.ParseUnixRights(&message)
			semanticErr = errors.Join(semanticErr, err)
			for _, fd := range rights {
				files = append(files, os.NewFile(uintptr(fd), "actual-original-controller-native-witness"))
			}
		} else if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_CREDENTIALS {
			credentials++
			current, err := unix.ParseUnixCredentials(&message)
			semanticErr = errors.Join(semanticErr, err)
			sender = current
		} else {
			semanticErr = errors.Join(semanticErr, gvisorHandoffFailureV2("controller witness has unsupported ancillary data"))
		}
	}
	complete := false
	defer func() {
		if !complete {
			closeFiles(files)
		}
	}()
	expectedRights := 0
	if count != 0 {
		expectedRights = 1
	}
	if parseErr != nil || semanticErr != nil || receivedFlags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n != len(expected) || !bytes.Equal(data[:n], expected) || len(files) != count || rightFrames != expectedRights || credentials != 1 || sender == nil || *sender != peer {
		return nil, errors.Join(parseErr, semanticErr, gvisorHandoffFailureV2("controller witness lacks its exact original current sender and descriptor vector"))
	}
	complete = true
	return files, nil
}
func requireGvisorControllerSetupEOFV1(file *os.File) error {
	data := make([]byte, 1)
	ancillary := make([]byte, unix.CmsgSpace(10*4)+unix.CmsgSpace(unix.SizeofUcred))
	n, oobn, flags, _, err := unix.Recvmsg(int(file.Fd()), data, ancillary, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return err
	}
	messages, parseErr := unix.ParseSocketControlMessage(ancillary[:oobn])
	var closeErr error
	for _, message := range messages {
		if message.Header.Level == unix.SOL_SOCKET && message.Header.Type == unix.SCM_RIGHTS {
			rights, err := unix.ParseUnixRights(&message)
			closeErr = errors.Join(closeErr, err)
			for _, fd := range rights {
				closeErr = errors.Join(closeErr, unix.Close(fd))
			}
		}
	}
	if err != nil || parseErr != nil || closeErr != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n != 0 || oobn != 0 {
		return errors.Join(err, parseErr, closeErr, gvisorHandoffFailureV2("the controller setup transport did not end after its sole exact public witness"))
	}
	return nil
}

func (owner *gvisorControllerOwnerV1) receiveWitnessV1() (failureErr error) {
	if owner.witness != nil || owner.setup == nil {
		return gvisorHandoffFailureV2("the original controller witness was omitted or reused")
	}
	native, err := owner.currentNativeV1()
	if err != nil {
		return err
	}
	defer native.Close()
	if err := gvisorControllerReadDeadlineV1(owner.setup); err != nil {
		return err
	}
	peer := gvisorControllerPeerV1(native.pid)
	files, err := receiveGvisorControllerPacketV1(owner.setup, []byte{'N', 'W', 'V', 1, 1}, 9, peer, true)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			closeFiles(files)
		}
	}()
	if err := requireGvisorControllerSetupEOFV1(owner.setup); err != nil {
		return err
	}
	if err := sameGvisorNativeObjectV1(files[1], owner.code); err != nil {
		return err
	}
	if err := checkMappedControllerCodeV1(files[1]); err != nil {
		return err
	}
	if err := checkGvisorCurrentCodeV1(native.task, owner.code); err != nil {
		return err
	}
	if err := checkGvisorControllerCurrentArgvV1(native.task); err != nil {
		return err
	}
	ownBirth, _, err := nativeGvisorTaskV1(files[2])
	if err != nil || ownBirth.pid != 1 || ownBirth.parent != 0 || ownBirth.birth != native.birth.birth {
		return errors.Join(err, gvisorHandoffFailureV2("the controller own-task witness differs from its original C kernel birth"))
	}
	if err := checkGvisorCurrentCodeV1(files[2], owner.code); err != nil {
		return err
	}
	if err := checkGvisorControllerNamespacesV1(native, files[2], files[3:]); err != nil {
		return err
	}
	endpoint := files[0]
	endpointPeer, peerErr := unix.GetsockoptUcred(int(endpoint.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	domain, domainErr := unix.GetsockoptInt(int(endpoint.Fd()), unix.SOL_SOCKET, unix.SO_DOMAIN)
	kind, kindErr := unix.GetsockoptInt(int(endpoint.Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	passcred, passErr := unix.GetsockoptInt(int(endpoint.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED)
	_, connectedErr := unix.Getpeername(int(endpoint.Fd()))
	if peerErr != nil || domainErr != nil || kindErr != nil || passErr != nil || connectedErr != nil || endpointPeer == nil || *endpointPeer != peer || domain != unix.AF_UNIX || kind != unix.SOCK_SEQPACKET || passcred != 1 {
		return errors.Join(peerErr, domainErr, kindErr, passErr, connectedErr, gvisorHandoffFailureV2("the controller private endpoint was not born in its original post-ND0 C"))
	}
	if err := gvisorControllerReadDeadlineV1(endpoint); err != nil {
		return err
	}
	after, _, err := nativeGvisorTaskV1(native.task)
	if err != nil || after != native.birth {
		return errors.Join(err, gvisorHandoffFailureV2("the original C task changed while joining its witness"))
	}
	if err := checkNativeLivePIDFDV1(int(native.creation.Fd())); err != nil {
		return err
	}
	if err := owner.setup.Close(); err != nil {
		return err
	}
	owner.setup = nil
	var endpointStat unix.Stat_t
	if err := unix.Fstat(int(endpoint.Fd()), &endpointStat); err != nil {
		return err
	}
	owner.witness = &gvisorControllerWitnessV1{owner: owner, endpoint: endpoint, executable: files[1], task: files[2], namespaces: files[3:], endpointDev: uint64(endpointStat.Dev), endpointIno: endpointStat.Ino}
	complete = true
	return nil
}

func (witness *gvisorControllerWitnessV1) currentV1() error {
	if witness == nil || witness.owner == nil || witness.owner.witness != witness || witness.endpoint == nil || witness.executable == nil || witness.task == nil || len(witness.namespaces) != 6 {
		return gvisorHandoffFailureV2("the original controller witness was copied or ended")
	}
	native, err := witness.owner.currentNativeV1()
	if err != nil {
		return err
	}
	defer native.Close()
	if err := errors.Join(sameGvisorNativeObjectV1(witness.executable, witness.owner.code), checkMappedControllerCodeV1(witness.executable), checkGvisorCurrentCodeV1(native.task, witness.owner.code), checkGvisorCurrentCodeV1(witness.task, witness.owner.code)); err != nil {
		return err
	}
	own, _, err := nativeGvisorTaskV1(witness.task)
	if err != nil || own.pid != 1 || own.parent != 0 || own.birth != native.birth.birth {
		return errors.Join(err, gvisorHandoffFailureV2("the original controller own-task birth changed"))
	}
	var endpoint unix.Stat_t
	if err := unix.Fstat(int(witness.endpoint.Fd()), &endpoint); err != nil || uint64(endpoint.Dev) != witness.endpointDev || endpoint.Ino != witness.endpointIno || endpoint.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errors.Join(err, gvisorHandoffFailureV2("the original controller private socket object changed"))
	}
	peer, err := unix.GetsockoptUcred(int(witness.endpoint.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || peer == nil || *peer != gvisorControllerPeerV1(native.pid) {
		return errors.Join(err, gvisorHandoffFailureV2("the original controller private socket creator changed"))
	}
	if err := checkGvisorControllerCurrentArgvV1(native.task); err != nil {
		return err
	}
	return checkGvisorControllerNamespacesV1(native, witness.task, witness.namespaces)
}
