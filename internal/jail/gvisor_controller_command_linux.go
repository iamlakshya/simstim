package jail

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// GvisorControllerInputV1 contains only already-owned public setup objects.
// It carries no guest, operation, command, environment or caller policy. The
// host factory separately requires its SAME protected signed runtime source.
type GvisorControllerInputV1 struct {
	ID, Role, HelperGuest                             string
	CPUs                                              int
	MemoryMax, HostMemory                             uint64
	Topology                                          *os.File
	Sources                                           []*os.File
	Helper                                            *os.File
	Runsc, ProcUmounter, Sentry, FDParking, Prewarmer *os.File
}

type gvisorControllerOwnerV1 struct {
	mu                 sync.Mutex
	original           *gvisorControllerOwnerV1
	command            *Cmd
	public             mappedControllerPublicV1
	files              []*os.File // public ONLY; no original guest descriptor
	setup, guest, code *os.File   // setup outside end, original withheld guest, same runsc
	witness            *gvisorControllerWitnessV1
	aggregate          *GvisorControllerAggregateV1
	closed             bool
}

func retainGvisorOriginalV1(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, gvisorHandoffFailureV2("the original native controller object is absent")
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 32)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "same-original-controller-object"), nil
}
func checkGvisorGuestStreamV1(file *os.File) error {
	if file == nil {
		return gvisorHandoffFailureV2("the original withheld guest stream is absent")
	}
	var st unix.Stat_t
	err := unix.Fstat(int(file.Fd()), &st)
	kind, kindErr := unix.GetsockoptInt(int(file.Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	domain, domainErr := unix.GetsockoptInt(int(file.Fd()), unix.SOL_SOCKET, unix.SO_DOMAIN)
	listener, listenErr := unix.GetsockoptInt(int(file.Fd()), unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	_, peerErr := unix.Getpeername(int(file.Fd()))
	if err != nil || kindErr != nil || domainErr != nil || listenErr != nil || peerErr != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || kind != unix.SOCK_STREAM || domain != unix.AF_UNIX || listener != 0 {
		return errors.Join(err, kindErr, domainErr, listenErr, peerErr, gvisorHandoffFailureV2("the original guest has another native stream role"))
	}
	return nil
}

// CommandGvisorControllerCapturedV1 builds the one closed fixed controller
// route. Guest is retained ONLY outside every native exec. Start creates the
// original C; AwaitGvisorControllerV1 joins real barriers, then the actual host
// operation owner must recheck its original authority before the one-use send.
// Spec accepts no caller argv/env/files; only its captured safe mount graph.
func CommandGvisorControllerCapturedV1(ctx context.Context, spec Spec, bootstrap *GvisorBootstrapV1, input GvisorControllerInputV1, guest *os.File) (_ *Cmd, failureErr error) {
	if ctx == nil || len(spec.Argv) != 0 || len(spec.Env) != 0 || len(spec.Files) != 0 || len(spec.FileRulesV1) != 0 || len(spec.Rules) != 0 {
		return nil, gvisorHandoffFailureV2("the fixed controller acquired caller command or file policy")
	}
	public := mappedControllerPublicV1{ID: input.ID, Role: input.Role, HelperGuest: input.HelperGuest, Sources: len(input.Sources), CPUs: input.CPUs, MemoryMax: input.MemoryMax, HostMemory: input.HostMemory}
	code, extent, err := mappedControllerExtentV1(public)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkMappedControllerTopologyV1(public, input.Topology, input.Sources); err != nil {
		return nil, err
	}
	if err := checkGvisorGuestStreamV1(guest); err != nil {
		return nil, err
	}
	owner := &gvisorControllerOwnerV1{public: public}
	owner.original = owner
	complete := false
	defer func() {
		if !complete {
			failureErr = errors.Join(failureErr, owner.closeV1())
		}
	}()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	owner.setup = os.NewFile(uintptr(pair[0]), "outside-original-controller-setup-only")
	owner.files = append(owner.files, os.NewFile(uintptr(pair[1]), "original-controller-setup-only"))
	for _, fd := range pair {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
			return nil, err
		}
	}
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	owner.files = append(owner.files, null) // exact harmless future state slot4
	appendOriginal := func(file *os.File) error {
		held, err := retainGvisorOriginalV1(file)
		if err != nil {
			return err
		}
		owner.files = append(owner.files, held)
		return nil
	}
	if err := appendOriginal(input.Topology); err != nil {
		return nil, err
	}
	for _, file := range input.Sources {
		if err := appendOriginal(file); err != nil {
			return nil, err
		}
	}
	if input.Role == "guest-init" {
		if err := appendOriginal(input.Helper); err != nil {
			return nil, err
		}
	} else if input.Helper != nil {
		return nil, gvisorHandoffFailureV2("the fixed smoke role acquired guest helper code")
	}
	for index, file := range []*os.File{input.ProcUmounter, input.Runsc, input.Sentry, input.FDParking, input.Prewarmer} {
		if index < 3 {
			err = checkMappedControllerCodeV1(file)
		} else {
			err = checkMappedControllerSealedCodeV1(file)
		}
		if err != nil {
			return nil, err
		}
		if err := appendOriginal(file); err != nil {
			return nil, err
		}
	}
	if len(owner.files)+3 != extent {
		return nil, gvisorHandoffFailureV2("the original closed controller layout changed")
	}
	owner.code = owner.files[code+1-3]
	owner.guest, err = retainGvisorOriginalV1(guest)
	if err != nil {
		return nil, err
	}
	// Native stream aliasing cannot smuggle a guest into the public setup slot.
	for _, file := range owner.files {
		left, err := file.Stat()
		right, rightErr := owner.guest.Stat()
		if err != nil || rightErr != nil || os.SameFile(left, right) {
			return nil, errors.Join(err, rightErr, gvisorHandoffFailureV2("the private guest aliases an inherited controller object"))
		}
	}
	spec.Argv = []string{"/proc/self/fd/" + strconv.Itoa(code+1), "native-controller-setup-v1"}
	spec.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"}
	spec.Files = slices.Clone(owner.files)
	command, err := commandGvisorPublicCapturedV1(ctx, spec, bootstrap)
	if err != nil {
		return nil, err
	}
	var set setup
	if err := json.Unmarshal(command.setup, &set); err != nil {
		command.closePins()
		return nil, errors.Join(err, command.gvisorOuter.closeCodeV1())
	}
	set.ControllerPublicV1 = &public
	command.setup, err = json.Marshal(set)
	if err != nil {
		command.closePins()
		return nil, errors.Join(err, command.gvisorOuter.closeCodeV1())
	}
	owner.command = command
	command.gvisorOuter.controller = owner
	complete = true
	return command, nil
}

// Caller must have established no child, or positively joined all original
// native creators including C. Closing a transport never grants that result.
func (owner *gvisorControllerOwnerV1) closeV1() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	owner.closed = true
	var result error
	for _, file := range append([]*os.File{owner.setup, owner.guest}, owner.files...) {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	if owner.witness != nil {
		result = errors.Join(result, owner.witness.closeV1())
	}
	owner.setup, owner.guest, owner.files = nil, nil, nil
	return result
}

// A and then C have kernel copies before their public setup release. Drop
// only the outside alias of that PUBLIC child transport, so its later EOF
// describes the transport. Code/guest/native ownership remains retained.
func (owner *gvisorControllerOwnerV1) releaseSetupAliasV1() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || len(owner.files) == 0 || owner.files[0] == nil {
		return gvisorHandoffFailureV2("the original public setup alias was omitted or reused")
	}
	file := owner.files[0]
	owner.files[0] = nil
	return file.Close()
}
