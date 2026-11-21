package jail

import (
	"errors"
	"os"

	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"golang.org/x/sys/unix"
)

// gvisorCommonScopeV1 is a non-serialisable success capability from the exact
// original A thread's real scope-only restriction. A caller bool or saved
// JSON cannot choose an unscoped filesystem layer. It lives only until A's
// later filesystem restriction succeeds, then all proof handles close.
type gvisorCommonScopeV1 struct {
	owner         *setup
	task          *os.File
	taskRef       os.FileInfo
	pid, tid, abi int
	applied       *gvisorScopeAppliedV1
}

type gvisorScopeAppliedV1 struct{ original *gvisorCommonScopeV1 }

func installGvisorCommonScopeV1(c *setup, abi int) (*gvisorCommonScopeV1, error) {
	if c == nil || !c.gvisorOuterNative || c.GvisorV1Version != 1 || !c.Userns || c.Sub != nil || c.Net != NetNone || c.Seccomp != profileGvisorV1 || c.gvisorCommonScope != nil {
		return nil, gvisorHandoffFailureV2("a common scope needs the original closed native A producer")
	}
	if err := checkGvisorOuterGateV1(c.gvisorOuterGate); err != nil {
		return nil, err
	}
	if abi < 6 {
		// These kernels have no IPC/signal scope feature. Generic newRuleset has
		// no scoped bits either; no unavailable feature is forged or omitted.
		return nil, nil
	}
	if os.Getpid() != 1 || os.Getppid() != 0 {
		return nil, gvisorHandoffFailureV2("the common scope is outside original A PID1")
	}
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || dumpable != 0 {
		return nil, errors.Join(err, gvisorHandoffFailureV2("the actual A thread is dumpable before common scope"))
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return nil, err
	}
	task, err := openGvisorProcV2("/proc/thread-self")
	if err != nil {
		return nil, err
	}
	reference, err := task.Stat()
	if err != nil {
		_ = task.Close()
		return nil, err
	}
	proof := &gvisorCommonScopeV1{owner: c, task: task, taskRef: reference, pid: os.Getpid(), tid: unix.Gettid(), abi: abi}
	attrs := ll.RulesetAttr{Scoped: ll.ScopeAbstractUnixSocket | ll.ScopeSignal}
	fd, err := ll.LandlockCreateRuleset(&attrs, 0)
	if err != nil {
		_ = task.Close()
		return nil, gvisorHandoffFailureV2("the kernel refused the original common IPC/signal scope")
	}
	scope := &ruleset{fd: fd, abi: abi}
	if err := scope.enforce(); err != nil {
		_ = task.Close()
		return nil, err
	}
	// Only this actual successful kernel restriction reaches the constructor.
	proof.applied = &gvisorScopeAppliedV1{original: proof}
	if err := proof.checkOriginal(c, abi); err != nil {
		_ = proof.Close()
		return nil, err
	}
	return proof, nil
}

func (proof *gvisorCommonScopeV1) checkOriginal(c *setup, abi int) error {
	if proof == nil || c == nil || proof.owner != c || proof.applied == nil || proof.applied.original != proof || proof.task == nil || proof.taskRef == nil || abi < 6 || proof.abi != abi || proof.pid != os.Getpid() || proof.tid != unix.Gettid() || !c.gvisorOuterNative || c.GvisorV1Version != 1 {
		return gvisorHandoffFailureV2("the filesystem layer has no original applied common-scope proof")
	}
	if err := checkGvisorOuterGateV1(c.gvisorOuterGate); err != nil {
		return err
	}
	actual, err := proof.task.Stat()
	if err != nil || !os.SameFile(proof.taskRef, actual) {
		return errors.Join(err, gvisorHandoffFailureV2("the original common-scope native task changed"))
	}
	current, err := openGvisorProcV2("/proc/thread-self")
	if err != nil {
		return err
	}
	native, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(proof.taskRef, native) {
		return errors.Join(statErr, closeErr, gvisorHandoffFailureV2("another native thread tried to reuse the common scope"))
	}
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil || nnp != 1 {
		return errors.Join(err, gvisorHandoffFailureV2("the original applied scope lost no_new_privs"))
	}
	return nil
}

func (proof *gvisorCommonScopeV1) Close() error {
	if proof == nil {
		return nil
	}
	task := proof.task
	proof.task, proof.taskRef, proof.owner, proof.applied = nil, nil, nil, nil
	if task != nil {
		return task.Close()
	}
	return nil
}

// newGvisorParentFilesystemRulesV1 omits no policy: the SAME original A thread
// already enforced both IPC/signal scope bits before creating C. This layer
// adds exactly the existing filesystem/network rights, without a NEW scope
// boundary that would wrongly separate A from its original child. Generic
// profiles and C's own commandRules still use newRuleset with their own scopes.
func newGvisorParentFilesystemRulesV1(c *setup, abi int) (*ruleset, error) {
	if c == nil {
		return nil, gvisorHandoffFailureV2("the scoped filesystem layer has no original A producer")
	}
	if err := c.gvisorCommonScope.checkOriginal(c, abi); err != nil {
		return nil, err
	}
	attrs := ll.RulesetAttr{HandledAccessFS: fsRights(abi), HandledAccessNet: netRights(abi, c.Net)}
	fd, err := ll.LandlockCreateRuleset(&attrs, 0)
	if err != nil {
		return nil, gvisorHandoffFailureV2("the original A filesystem ruleset cannot be created")
	}
	return &ruleset{fd: fd, abi: abi}, nil
}
