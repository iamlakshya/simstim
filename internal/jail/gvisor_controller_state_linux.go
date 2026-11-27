package jail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// These are public fixed-role DATA, never an original native creation or
// permission for private input. The same outside protected suite and original
// C owner must still bind every object before accepting final witnesses.
type mappedControllerPublicV1 struct {
	ID          string `json:"id"`
	Role        string `json:"role"`
	HelperGuest string `json:"helper_guest"`
	Sources     int    `json:"sources"`
	CPUs        int    `json:"cpus"`
	MemoryMax   uint64 `json:"memory_max"`
	HostMemory  uint64 `json:"host_memory"`
}
type mappedControllerObjectV1 struct {
	FD  int    `json:"fd"`
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}
type mappedControllerBootV1 struct {
	Version      int                        `json:"v"`
	ID           string                     `json:"id"`
	Role         string                     `json:"role"`
	CPUs         int                        `json:"cpus"`
	MemoryMax    uint64                     `json:"memory_max"`
	HostMemory   uint64                     `json:"host_memory"`
	Topology     mappedControllerObjectV1   `json:"topology"`
	Sources      []mappedControllerObjectV1 `json:"sources"`
	Helper       *mappedControllerObjectV1  `json:"helper,omitempty"`
	HelperGuest  string                     `json:"helper_guest"`
	HelperDigest string                     `json:"helper_digest"`
	RootDev      uint64                     `json:"root_dev"`
	RootIno      uint64                     `json:"root_ino"`
	ProgramDev   uint64                     `json:"program_dev"`
	ProgramIno   uint64                     `json:"program_ino"`
}
type mappedControllerBaseV1 struct {
	Version      int                      `json:"v"`
	Boot         json.RawMessage          `json:"boot"`
	ProcUmounter mappedControllerObjectV1 `json:"proc_umounter"`
	Program      mappedControllerObjectV1 `json:"program"`
}
type mappedControllerStateV1 struct {
	Version   int                      `json:"v"`
	Setup     json.RawMessage          `json:"setup"`
	Sentry    mappedControllerObjectV1 `json:"sentry"`
	FDParking mappedControllerObjectV1 `json:"fdparking"`
	Prewarmer mappedControllerObjectV1 `json:"prewarmer"`
}

func mappedControllerExtentV1(public mappedControllerPublicV1) (code, extent int, err error) {
	if !api.ValidSessionID(public.ID) || public.Sources < 0 || public.Sources > 1000 || public.CPUs < 1 || public.CPUs > 256 || public.MemoryMax == 0 || public.HostMemory == 0 || public.MemoryMax > 1<<60 || public.HostMemory > 1<<60 {
		return 0, 0, gvisorHandoffFailureV2("the controller public resource or topology shape changed")
	}
	code = 6 + public.Sources
	switch public.Role {
	case "guest-init":
		if public.HelperGuest != path.Join("/opt", brand.Name, "bin", brand.HostBinary) {
			return 0, 0, gvisorHandoffFailureV2("controller setup selected another fixed guest helper")
		}
		code++
	case "probe-true":
		if public.HelperGuest != "" {
			return 0, 0, gvisorHandoffFailureV2("the fixed smoke role acquired a guest helper")
		}
	default:
		return 0, 0, gvisorHandoffFailureV2("controller setup acquired another guest program")
	}
	extent = code + 5
	if extent > 1014 {
		return 0, 0, gvisorHandoffFailureV2("controller public objects overlap the final raw-entry namespace slots")
	}
	return code, extent, nil
}

func mappedControllerObjectAtV1(file *os.File, target int) (mappedControllerObjectV1, error) {
	var st unix.Stat_t
	if file == nil {
		return mappedControllerObjectV1{}, gvisorHandoffFailureV2("a controller public object is absent")
	}
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return mappedControllerObjectV1{}, err
	}
	return mappedControllerObjectV1{FD: target, Dev: uint64(st.Dev), Ino: st.Ino}, nil
}
func mappedControllerNullV1(file *os.File) error {
	var st unix.Stat_t
	if file == nil {
		return gvisorHandoffFailureV2("controller stdio or state placeholder is absent")
	}
	err := unix.Fstat(int(file.Fd()), &st)
	flags, flagErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flagErr != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(st.Rdev) != 1 || unix.Minor(st.Rdev) != 3 || flags&unix.O_ACCMODE != unix.O_RDWR {
		return errors.Join(err, flagErr, gvisorHandoffFailureV2("controller stdio or state slot is not its original harmless null device"))
	}
	return nil
}

// sealMappedControllerStateV1 writes the exact runtime consumer's fixed wire.
// Files are SAME already-owned public native objects in final slot order:
// null0..2, setup3, null4, topology5, sources6.., optional helper, five codes.
// No guest stream, OP, argv, environment or raw OCI can enter this state.
func sealMappedControllerStateV1(public mappedControllerPublicV1, files []*os.File) (_ *os.File, failureErr error) {
	code, extent, err := mappedControllerExtentV1(public)
	if err != nil {
		return nil, err
	}
	if len(files) != extent {
		return nil, gvisorHandoffFailureV2("the controller public descriptor vector has another exact extent")
	}
	for _, slot := range []int{0, 1, 2, 4} {
		if err := mappedControllerNullV1(files[slot]); err != nil {
			return nil, err
		}
	}
	if files[3] == nil {
		return nil, gvisorHandoffFailureV2("the original setup-only controller stream is missing")
	}
	kind, typeErr := unix.GetsockoptInt(int(files[3].Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	domain, domainErr := unix.GetsockoptInt(int(files[3].Fd()), unix.SOL_SOCKET, unix.SO_DOMAIN)
	_, peerErr := unix.Getpeername(int(files[3].Fd()))
	if typeErr != nil || domainErr != nil || peerErr != nil || kind != unix.SOCK_STREAM || domain != unix.AF_UNIX {
		return nil, errors.Join(typeErr, domainErr, peerErr, gvisorHandoffFailureV2("controller setup has another native stream role"))
	}
	if err := checkMappedControllerTopologyV1(public, files[5], files[6:6+public.Sources]); err != nil {
		return nil, err
	}
	boot := mappedControllerBootV1{Version: 1, ID: public.ID, Role: public.Role, CPUs: public.CPUs, MemoryMax: public.MemoryMax, HostMemory: public.HostMemory, Sources: []mappedControllerObjectV1{}, HelperGuest: public.HelperGuest}
	boot.Topology, err = mappedControllerObjectAtV1(files[5], 5)
	if err != nil {
		return nil, err
	}
	for index := range public.Sources {
		object, err := mappedControllerObjectAtV1(files[6+index], 6+index)
		if err != nil {
			return nil, err
		}
		boot.Sources = append(boot.Sources, object)
	}
	if public.Role == "guest-init" {
		helper := files[code-1]
		object, err := mappedControllerObjectAtV1(helper, code-1)
		if err != nil {
			return nil, err
		}
		info, err := helper.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 512<<20 || info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			return nil, errors.Join(err, gvisorHandoffFailureV2("controller setup lost its original fixed helper code"))
		}
		digest := sha256.New()
		n, err := io.Copy(digest, io.NewSectionReader(helper, 0, info.Size()))
		if err != nil || n != info.Size() {
			return nil, errors.Join(err, io.ErrUnexpectedEOF)
		}
		boot.Helper, boot.HelperDigest = &object, hex.EncodeToString(digest.Sum(nil))
	}
	objects := make([]mappedControllerObjectV1, 5)
	seen := map[[2]uint64]bool{}
	for index := range 5 {
		if index < 3 {
			err = checkMappedControllerCodeV1(files[code+index])
		} else {
			err = checkMappedControllerSealedCodeV1(files[code+index])
		}
		if err != nil {
			return nil, err
		}
		objects[index], err = mappedControllerObjectAtV1(files[code+index], code+index)
		if err != nil {
			return nil, err
		}
		key := [2]uint64{objects[index].Dev, objects[index].Ino}
		if seen[key] {
			return nil, gvisorHandoffFailureV2("controller setup aliases distinct original signed code roles")
		}
		seen[key] = true
	}
	var root unix.Stat_t
	if err := unix.Stat("/", &root); err != nil {
		return nil, err
	}
	boot.RootDev, boot.RootIno, boot.ProgramDev, boot.ProgramIno = uint64(root.Dev), root.Ino, objects[1].Dev, objects[1].Ino
	rawBoot, err := json.Marshal(boot)
	if err != nil {
		return nil, err
	}
	rawBase, err := json.Marshal(mappedControllerBaseV1{Version: 1, Boot: rawBoot, ProcUmounter: objects[0], Program: objects[1]})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(mappedControllerStateV1{Version: 1, Setup: rawBase, Sentry: objects[2], FDParking: objects[3], Prewarmer: objects[4]})
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.Join(err, gvisorHandoffFailureV2("the controller sealed public state exceeds its closed bound"))
	}
	fd, err := unix.MemfdCreate("mapped-controller-public-state-v1", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	state := os.NewFile(uintptr(fd), "original-sealed-controller-public-state")
	complete := false
	defer func() {
		if !complete {
			failureErr = errors.Join(failureErr, state.Close())
		}
	}()
	if n, err := state.Write(raw); err != nil || n != len(raw) {
		return nil, errors.Join(err, io.ErrShortWrite)
	}
	required := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(state.Fd(), unix.F_ADD_SEALS, required); err != nil {
		return nil, err
	}
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	complete = true
	return state, nil
}

// This body is ONLY for the original mapped C fixed entry, after its native
// ancestry proof. The outside producer/consumer route must be joined before
// any call site can select it. Ordinary runExec never calls this function.
func execMappedGvisorControllerV1(public mappedControllerPublicV1) (failureErr error) {
	runtime.LockOSThread()
	// Actual PID1, one-ID map, empty groups, ND0 and bounded namespace caps.
	if err := prepareGvisorBootstrapV1(); err != nil {
		return err
	}
	code, extent, err := mappedControllerExtentV1(public)
	if err != nil {
		return err
	}
	files := make([]*os.File, 0, extent)
	defer func() {
		for _, file := range files {
			failureErr = errors.Join(failureErr, file.Close())
		}
	}()
	for slot := range extent {
		fd, err := unix.FcntlInt(uintptr(slot), unix.F_DUPFD_CLOEXEC, extent+32)
		if err != nil {
			return err
		}
		files = append(files, os.NewFile(uintptr(fd), "same-original-mapped-controller-public-object"))
	}
	state, err := sealMappedControllerStateV1(public, files)
	if err != nil {
		return err
	}
	defer state.Close()
	// Recheck every current raw destination against the held objects immediately
	// before replacing only its reserved null state slot. No new native source
	// is obtained from a pathname or from a failed executable observation.
	for slot, held := range files {
		current := mappedControllerObjectV1{FD: slot}
		var st unix.Stat_t
		if err := unix.Fstat(slot, &st); err != nil {
			return err
		}
		current.Dev, current.Ino = uint64(st.Dev), st.Ino
		expected, err := mappedControllerObjectAtV1(held, slot)
		if err != nil || current != expected {
			return errors.Join(err, gvisorHandoffFailureV2("the original mapped controller vector changed before its fixed exec"))
		}
	}
	if err := unix.Dup3(int(state.Fd()), 4, 0); err != nil {
		return err
	}
	if err := cloexecFrom(extent); err != nil {
		return err
	}
	for slot := range extent {
		if _, err := unix.FcntlInt(uintptr(slot), unix.F_SETFD, 0); err != nil {
			return err
		}
	}
	// Exact held static runsc, fixed internal role, fixed public environment.
	// Its raw entry establishes ND0/common scope before Go. Sentry's separate
	// same-process final reexec and raw final filesystem boundary are unchanged.
	name := "native-controller-setup-v1"
	err = execveat(code+1, []string{name, name}, []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TZ=UTC"})
	return gvisorHandoffFailureV2(fmt.Sprintf("the original held controller exec failed: %v", err))
}

func checkMappedControllerTopologyV1(public mappedControllerPublicV1, descriptor *os.File, sources []*os.File) error {
	if descriptor == nil {
		return gvisorHandoffFailureV2("the original controller topology descriptor is absent")
	}
	required := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	seals, err := unix.FcntlInt(descriptor.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&required != required {
		return errors.Join(err, gvisorHandoffFailureV2("controller topology lost its original immutable seals"))
	}
	data, err := io.ReadAll(io.NewSectionReader(descriptor, 0, (4<<20)+1))
	if err != nil || len(data) < 1 || len(data) > 4<<20 {
		return errors.Join(err, gvisorHandoffFailureV2("controller topology exceeds its closed byte bound"))
	}
	fields, err := api.JSONObjectFields(data, "v", "id", "role", "mounts")
	if err != nil || len(fields) != 4 || bytes.Equal(bytes.TrimSpace(fields["mounts"]), []byte("null")) {
		return errors.Join(err, gvisorHandoffFailureV2("controller topology changed its exact public fields"))
	}
	var wire struct {
		Version int               `json:"v"`
		ID      string            `json:"id"`
		Role    string            `json:"role"`
		Mounts  []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Version != 2 || wire.ID != public.ID || wire.Role != public.Role || len(wire.Mounts) != len(sources) {
		return gvisorHandoffFailureV2("controller topology differs from its original fixed role or native source count")
	}
	for index, raw := range wire.Mounts {
		fields, err := api.JSONObjectFields(raw, "role", "source", "guest", "write", "identity")
		if err != nil || len(fields) != 5 {
			return errors.Join(err, gvisorHandoffFailureV2("a controller mount gained another public field"))
		}
		for _, field := range fields {
			if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return gvisorHandoffFailureV2("a controller topology field became null")
			}
		}
		var source struct {
			Source   int    `json:"source"`
			Identity string `json:"identity"`
		}
		if err := json.Unmarshal(raw, &source); err != nil {
			return err
		}
		object, err := mappedControllerObjectAtV1(sources[index], 6+index)
		if err != nil || source.Source != index || source.Identity != fmt.Sprintf("%x:%x", object.Dev, object.Ino) {
			return errors.Join(err, gvisorHandoffFailureV2("controller topology substituted an original native source object"))
		}
	}
	return nil
}
