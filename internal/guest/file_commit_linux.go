//go:build linux

package guest

import (
	"errors"
	"os"
	"syscall"

	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

func (g *Init) fileCommitProcess(run *execRun) (*syscall.SysProcAttr, error) {
	vm, ok := g.vm.(*vmRuntime)
	if !ok || run.fileCommit == nil || run.fileCommit.owner != g || len(run.fileCommit.files) != 4 || run.inputBundle != nil || run.kind != "" || run.vmRole != vmFileCommitCommand {
		return nil, putError("file publication has no original private VM owner")
	}
	if err := vm.connection.check(g); err != nil {
		return nil, err
	}
	executable, err := g.helperExecutable()
	if err != nil {
		return nil, err
	}
	if len(run.args.Argv) != 2 || run.args.Argv[0] != executable || run.args.Argv[1] != VMFileCommitHelperMode || run.args.Cwd != "/" {
		return nil, putError("file publication is not its fixed helper role")
	}
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}, nil
}

func dropFileCommitAccount() error {
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return putError("the private VM publisher was not created by actual guest root")
	}
	if err := secretmem.Protect(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	for cap := 0; cap < 64; cap++ {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(cap), 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
		return err
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return err
	}
	if err := syscall.Setresgid(AgentGID, AgentGID, AgentGID); err != nil {
		return err
	}
	if err := syscall.Setresuid(AgentUID, AgentUID, AgentUID); err != nil {
		return err
	}
	if err := secretmem.Protect(); err != nil {
		return err
	}
	if err := checkInputHelperAccount(); err != nil {
		return err
	}
	state, err := secretmem.Inspect()
	if err != nil {
		return err
	}
	if state.Dumpable == nil || *state.Dumpable != 0 {
		return putError("the private file publisher is not non-dumpable after its account transition")
	}
	if err := fileCommitUntraced(); err != nil {
		return err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var caps [2]unix.CapUserData
	if err := unix.Capset(&header, &caps[0]); err != nil {
		return err
	}
	return nil
}
