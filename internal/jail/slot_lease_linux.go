package jail

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/slotlease"
	"golang.org/x/sys/unix"
)

const stageSlotInitLeaseV1 = "slot-init-lease-v1"

type slotInitLeaseRefV1 struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

// CommandSlotV1 consumes only the actual root allocator's opaque native keeper
// handoff. No UID/GID, descriptor, data flag or callback can select this role.
// This retains an OFD in trusted init only; it is NOT whole-slot vacancy or
// reallocation authority. The root allocator must apply its post-lock native
// nondelegated-cgroup/events fence and quarantine all uncertainty.
func CommandSlotV1(ctx context.Context, s Spec, source *slotlease.SourceV1) (*Cmd, error) {
	if ctx == nil || source == nil {
		return nil, slotLeaseFailureV1("the actual root slot source is absent")
	}
	if s.Subuid || s.gvisorV1 || s.Seccomp != ProfileVMM && s.Seccomp != ProfileConverter {
		return nil, slotLeaseFailureV1("the slot role is not its fixed VMM or converter purpose")
	}
	if err := source.CheckCurrentV1(ctx); err != nil {
		return nil, err
	}
	lease, err := source.RetainForInitV1()
	if err != nil {
		return nil, err
	}
	ref, err := captureSlotInitLeaseRefV1(lease)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	command, err := Command(ctx, s)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	fail := func(cause error) (*Cmd, error) { command.closePins(); return nil, errors.Join(cause, lease.Close()) }
	attr := command.cmd.SysProcAttr
	if attr == nil || attr.Cloneflags&unix.CLONE_NEWUSER == 0 || attr.Cloneflags&unix.CLONE_NEWPID == 0 {
		return fail(slotLeaseFailureV1("this slot requires actual user and PID namespaces; a fallback cannot own its lifetime"))
	}
	var set setup
	if err := json.Unmarshal(command.setup, &set); err != nil {
		return fail(err)
	}
	set.InitLeaseV1Version, set.InitLeaseFDV1, set.InitLeaseRefV1 = 1, 5+len(command.files)+len(command.pins), ref
	encoded, err := json.Marshal(set)
	if err != nil {
		return fail(err)
	}
	command.setup = encoded
	command.cmd.Args[1] = fileRulesStageV1(stageInit, &set)
	command.slotSourceV1, command.slotInitLeaseV1, command.slotInitLeaseRefV1 = source, lease, ref
	return command, nil
}

func captureSlotInitLeaseRefV1(file *os.File) (*slotInitLeaseRefV1, error) {
	if file == nil {
		return nil, slotLeaseFailureV1("the retained slot OFD is missing")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return nil, err
	}
	if st.Uid != 0 {
		return nil, slotLeaseFailureV1("the actual keeper lease is not owned by host root")
	}
	ref := &slotInitLeaseRefV1{Dev: st.Dev, Ino: st.Ino}
	if err := checkSlotInitLeaseFileV1(file, ref); err != nil {
		return nil, err
	}
	return ref, nil
}

func checkSlotInitLeaseFileV1(file *os.File, ref *slotInitLeaseRefV1) error {
	if file == nil || ref == nil {
		return slotLeaseFailureV1("the slot init lease has no original native identity")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0644 || st.Nlink != 1 || st.Dev != ref.Dev || st.Ino != ref.Ino || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.Join(err, slotLeaseFailureV1("the original root readonly0644 lease object changed"))
	}
	return nil
}

func checkSlotInitLeaseStageV1(stage string, c *setup) (string, error) {
	if c == nil {
		return "", slotLeaseFailureV1("the init lease stage has no setup")
	}
	if c.InitLeaseV1Version == 0 {
		if c.InitLeaseFDV1 != 0 || c.InitLeaseRefV1 != nil || stage == stageSlotInitLeaseV1 {
			return "", slotLeaseFailureV1("an old stage cannot carry the root init lease")
		}
		return stage, nil
	}
	if stage != stageSlotInitLeaseV1 || c.InitLeaseV1Version != 1 || c.InitLeaseRefV1 == nil || c.InitLeaseFDV1 != 5+c.Files+len(c.Binds)+len(pinnedRules(c.Rules)) || c.GvisorV1Version != 0 || c.ControllerPublicV1 != nil || !c.Userns || c.Sub != nil || c.Seccomp != ProfileVMM && c.Seccomp != ProfileConverter {
		return "", slotLeaseFailureV1("the closed slot init lease version, extent or namespace role changed")
	}
	return baseFileRulesStageV1(stageInit, c), nil
}

func holdSlotInitLeaseV1(c *setup) (*os.File, error) {
	if c.InitLeaseV1Version == 0 {
		return nil, nil
	}
	file := os.NewFile(uintptr(c.InitLeaseFDV1), "root-slot-init-only-lease")
	// Host root ownership was proven by the actual keeper source before
	// namespaces. Inside the one-slot UID map, host UID0 may be overflow;
	// exact held inode/mode/OFD identity remains the native role constraint.
	if err := checkSlotInitLeaseFileV1(file, c.InitLeaseRefV1); err != nil {
		_ = file.Close()
		return nil, err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		_ = file.Close()
		return nil, errors.Join(err, slotLeaseFailureV1("the init-only root lease could reach final exec"))
	}
	return file, nil
}

func setupForFinalExecV1(c *setup) setup {
	next := *c
	next.StatusFD, next.SyncFD, next.Sub = 0, 0, nil
	next.InitLeaseV1Version, next.InitLeaseFDV1, next.InitLeaseRefV1 = 0, 0, nil
	return next
}

// RetainSlotInitV1 exposes the SAME original creation pidfd as native evidence
// only. Generic commands, copied sources, saved IDs and dead tasks cannot
// produce it. Root's guest/native binding still needs its exact full proof.
func (c *Cmd) RetainSlotInitV1(source *slotlease.SourceV1) (*os.File, error) {
	if c == nil || source == nil || c.slotSourceV1 != source {
		return nil, slotLeaseFailureV1("the command has no same original slot source")
	}
	if err := source.CheckCurrentV1(c.ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.owned || c.reaped || c.physical || c.pidfd < 0 || c.phase != phaseRunning && c.phase != phaseWaiting && c.phase != phaseUncertain {
		return nil, slotLeaseFailureV1("the original slot init is not live and owned")
	}
	ready, err := ownedPIDFDReady(c.pidfd, 0)
	if err != nil || ready {
		return nil, errors.Join(err, slotLeaseFailureV1("the original native slot init ended"))
	}
	fd, err := unix.FcntlInt(uintptr(c.pidfd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "same-creation-slot-init-evidence"), nil
}

func slotLeaseFailureV1(message string) error {
	return failure("the fixed slot init lease refused: "+message, "Use the same actual root allocator and keeper; retain the slot and native resources until whole-slot vacancy is proven.")
}
