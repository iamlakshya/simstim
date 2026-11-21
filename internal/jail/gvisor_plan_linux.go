package jail

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// receiveGvisorPlanV2 is the same native vector consumer for the initial A
// helper and nested C helper. Both reach it only after actual ND0 and their
// exact native ancestry proof; no pre-map endpoint can call this receiver.
func receiveGvisorPlanV2(conn *net.UnixConn, peer unix.Ucred, stage string, protected **os.File, slotCount int) (*setup, error) {
	return receiveGvisorPlanControlV2(conn, peer, stage, protected, slotCount, false)
}

// Retention is selected only by the original post-map outer entry. It keeps
// this actual socket object outside the serialised setup and future Files.
func receiveGvisorPlanControlV2(conn *net.UnixConn, peer unix.Ucred, stage string, protected **os.File, slotCount int, retainControl bool) (*setup, error) {
	if retainControl && stage != stageInit {
		return nil, gvisorHandoffFailureV2("only the original mapped init may retain its actual control")
	}
	plan, _, err := receiveGvisorV2(conn, peer, "plan", 0)
	if err != nil {
		return nil, err
	}
	if plan.Setup == nil || len(plan.Slots) < 3 || len(plan.Slots) > maxGvisorSlotsV2 {
		return nil, gvisorHandoffFailureV2("the proved parent omitted its exact setup vector")
	}
	c := plan.Setup
	normal, err := checkFileRulesStageV1(fileRulesStageV1(stage, c), c)
	if err != nil || normal != stage || c.Files < 0 || c.Files+3 > maxGvisorSlotsV2 {
		return nil, errors.Join(err, gvisorHandoffFailureV2("the released setup has another native purpose"))
	}
	seen := make(map[int]bool)
	highestSlot := 0
	for _, slot := range plan.Slots {
		if slot < 0 || slot >= maxGvisorSlotsV2 || seen[slot] {
			return nil, gvisorHandoffFailureV2("the released native vector repeats or exceeds its fixed slot")
		}
		seen[slot] = true
		highestSlot = max(highestSlot, slot)
	}
	if highestSlot+1 != slotCount {
		return nil, gvisorHandoffFailureV2("the released vector has another original reserved extent")
	}
	expected, err := expectedGvisorSlotsV1(c, stage)
	if err != nil || !equalGvisorSlotsV2(plan.Slots, expected) {
		return nil, errors.Join(err, gvisorHandoffFailureV2("the released native vector has extra, missing or changed closed roles"))
	}
	floor := highestSlot + 32
	if protected != nil && *protected != nil {
		fd, err := unix.FcntlInt((*protected).Fd(), unix.F_DUPFD_CLOEXEC, floor)
		if err != nil {
			return nil, err
		}
		if err := (*protected).Close(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		*protected = os.NewFile(uintptr(fd), "retained-gvisor-original-creator-gate")
	}
	held := make([]*os.File, 0, len(plan.Slots))
	defer func() { closeFiles(held) }()
	for offset := 0; offset < len(plan.Slots); offset += 8 {
		end := min(offset+8, len(plan.Slots))
		batch, files, err := receiveGvisorV2(conn, peer, "files", end-offset)
		if err != nil {
			return nil, err
		}
		if !equalGvisorSlotsV2(batch.Slots, plan.Slots[offset:end]) {
			closeFiles(files)
			return nil, gvisorHandoffFailureV2("a native descriptor batch has different fixed roles")
		}
		// Keep receive objects above every destination. No raw source is confused
		// with a placeholder, proof file or the still-live control connection.
		for _, file := range files {
			fd, dupErr := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, floor)
			closeErr := file.Close()
			if dupErr != nil || closeErr != nil {
				closeFiles(files)
				return nil, errors.Join(dupErr, closeErr)
			}
			held = append(held, os.NewFile(uintptr(fd), "held-gvisor-role"))
		}
	}
	if _, _, err = receiveGvisorV2(conn, peer, "release", 0); err != nil {
		return nil, err
	}
	if !retainControl {
		if err = conn.Close(); err != nil {
			return nil, err
		}
	}
	for index, source := range held {
		if err = unix.Dup3(int(source.Fd()), plan.Slots[index], unix.O_CLOEXEC); err != nil {
			return nil, err
		}
	}
	for fd := 0; fd < 3+c.Files; fd++ {
		if !seen[fd] {
			return nil, gvisorHandoffFailureV2("the original stdio or Files slot is missing")
		}
		if _, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return nil, err
		}
	}
	closeFiles(held)
	held = nil
	if err = checkStageFiles(c.Files); err != nil {
		return nil, err
	}
	if err = checkStageFileRulesV1(c); err != nil {
		return nil, err
	}
	if retainControl {
		c.gvisorReport = conn
	}
	return c, nil
}
