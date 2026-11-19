package guest

import (
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type vmCommandRole uint8

const (
	vmAgentCommand vmCommandRole = iota
	vmSeedCommand
	vmDockerCommand
	vmFileCommitCommand
)

// vmHooks is the Linux VM producer in the ONE ordinary guest engine. It
// configures identity/IO/filesystems; startOwned/waitOwned remain its sole
// child ownership machinery. A process guest has no such hook object.
type vmHooks interface {
	controlOperation() (func(), error)
	seedGit(string, []string) error
	seedStream(*link.Stream) (func(), error)
	holderStream(*link.Stream, net.Conn) (func(), error)
	childEnvironment([]string) []string
	seedDirectory(string) error
	seedFile(*os.File) error
	prepare(proto.CtlVMPrepareArgs) (proto.CtlVMPrepareResult, error)
	prepared() bool
	ptyABI() string
	agentProcess() (*syscall.SysProcAttr, error)
	holderProcess() (*syscall.SysProcAttr, error)
	holderFiles(string) host.GuestHolder
	startHolderCommand(*exec.Cmd) error
	helperExecutable() (string, error)
	configureHolder(*exec.Cmd, string, string, host.GuestHolder) (func(), error)
	validateSession(proto.CtlStartArgs) error
	setupDNS(string) error
	memberProxyEnv(*proto.CtlStartArgs, bool) error
	validateMember(proto.CtlStartArgs, bool) error
	startAgent(proto.CtlStartArgs, *holderProc) (int, error)
	beforeOwnedStart(*execRun, *exec.Cmd) (func(), error)
	beforeOwnedReap(*execRun)
	afterOwnedReap(*execRun)
	stopMember(string) error
	stop(time.Duration)
	memoryClaim() *proto.GuestMemoryV1
	beginStop()
	joinPTY() bool
	cleanupFiles() error
	capture(bool) error
	shutdown() error
}

func (g *Init) commandProcess() (*syscall.SysProcAttr, error) {
	if g.vm != nil {
		return g.vm.agentProcess()
	}
	return agentProcess()
}

func (g *Init) helperExecutable() (string, error) {
	if g.vm != nil {
		return g.vm.helperExecutable()
	}
	return os.Executable()
}
