package jail

import (
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

func parseGvisorSlotPreludeV1(data []byte) (int, error) {
	if len(data) != 8 || data[0] != 'S' || data[1] != 'L' || data[2] != 'T' || data[3] != 1 {
		return 0, gvisorHandoffFailureV2("the fixed public native slot prelude has another version or shape")
	}
	count := binary.LittleEndian.Uint32(data[4:])
	if count < 6 || count > maxGvisorSlotsV2 {
		return 0, gvisorHandoffFailureV2("the original native future slot count exceeds its closed bound")
	}
	return int(count), nil
}

// consumeGvisorSlotPreludeV1 performs NO open/FileConn allocation before the
// original two control objects are high and their old slots refilled. This is
// the fixed PUBLIC fd5 prelude from the same original C creation controller.
// Count is a layout constraint only, never a namespace or private-right grant.
func consumeGvisorSlotPreludeV1() (count, control, gate int, failure error) {
	control, gate = -1, -1
	var data [8]byte
	offset := 0
	for offset < len(data) {
		n, err := unix.Read(5, data[offset:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, -1, -1, err
		}
		if n <= 0 {
			return 0, -1, -1, io.ErrUnexpectedEOF
		}
		offset += n
	}
	count, err := parseGvisorSlotPreludeV1(data[:])
	if err != nil {
		return 0, -1, -1, err
	}
	if err := verifyGvisorNullVectorV1(count, true); err != nil {
		return 0, -1, -1, err
	}
	control, err = unix.FcntlInt(3, unix.F_DUPFD_CLOEXEC, count+32)
	if err != nil {
		return 0, -1, -1, err
	}
	if err = unix.Dup3(0, 3, 0); err != nil {
		_ = unix.Close(control)
		return 0, -1, -1, err
	}
	gate, err = unix.FcntlInt(5, unix.F_DUPFD_CLOEXEC, count+32)
	if err != nil {
		_ = unix.Close(control)
		return 0, -1, -1, err
	}
	if err = unix.Dup3(0, 5, 0); err != nil {
		_ = unix.Close(control)
		_ = unix.Close(gate)
		return 0, -1, -1, err
	}
	return count, control, gate, nil
}

func verifyGvisorNullVectorV1(count int, outerControls bool) error {
	if count < 6 || count > maxGvisorSlotsV2 {
		return gvisorHandoffFailureV2("the original null vector has no exact bounded extent")
	}
	for fd := 0; fd < count; fd++ {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		if outerControls && fd == 3 {
			if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
				return gvisorHandoffFailureV2("the fixed post-map control has another native descriptor role")
			}
			kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
			if err != nil || kind != unix.SOCK_STREAM {
				return errors.Join(err, gvisorHandoffFailureV2("the fixed control is not its original native stream"))
			}
			domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
			if err != nil || domain != unix.AF_UNIX {
				return errors.Join(err, gvisorHandoffFailureV2("the fixed control is outside its native Unix domain"))
			}
			continue
		}
		if outerControls && fd == 5 {
			if st.Mode&unix.S_IFMT != unix.S_IFIFO {
				return gvisorHandoffFailureV2("the C death gate has another native role")
			}
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(st.Rdev) != 1 || unix.Minor(st.Rdev) != 3 || flags&unix.O_ACCMODE != unix.O_RDWR {
			return errors.Join(err, gvisorHandoffFailureV2("a future native descriptor slot is not the exact harmless null placeholder"))
		}
	}
	return nil
}

// expectedGvisorSlotsV1 derives roles from the original closed setup. No
// received vector may insert extra low descriptors or omit a bind/rule role.
func expectedGvisorSlotsV1(c *setup, stage string) ([]int, error) {
	if c == nil || c.Files < 0 || c.Files > maxGvisorSlotsV2-5 || c.GvisorV1Version != 1 || c.SyncFD != 0 || c.Sub != nil {
		return nil, gvisorHandoffFailureV2("the native role layout has another closed setup purpose")
	}
	result := []int{0, 1, 2}
	for index := range c.Files {
		result = append(result, 3+index)
	}
	switch stage {
	case stageInit:
		if c.StatusFD != 4+c.Files {
			return nil, gvisorHandoffFailureV2("the original init status role moved")
		}
		result = append(result, c.StatusFD)
	case stageExec:
		if c.StatusFD != 0 {
			return nil, gvisorHandoffFailureV2("the nested runtime gained a parent status role")
		}
	default:
		return nil, gvisorHandoffFailureV2("the native role vector has another exec stage")
	}
	for _, bound := range c.Binds {
		result = append(result, bound.FD)
	}
	for _, rule := range pinnedRules(c.Rules) {
		result = append(result, rule.FD)
	}
	seen := map[int]bool{}
	for _, fd := range result {
		if fd < 0 || fd >= maxGvisorSlotsV2 || seen[fd] {
			return nil, gvisorHandoffFailureV2("the original role layout repeats or exceeds its exact slot")
		}
		seen[fd] = true
	}
	return result, nil
}
