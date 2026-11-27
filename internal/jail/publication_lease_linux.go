//go:build linux

package jail

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/iamlakshya/simstim/internal/publicationlease"
	"golang.org/x/sys/unix"
)

type publicationExecLeaseV1 struct {
	file   *os.File
	record string
	fd     int
}
type publicationLeaseSetupV1 struct {
	Version int    `json:"v"`
	FD      int    `json:"fd"`
	Record  string `json:"record"`
}

const stageInitPublicationV1 = "init-publication-v1"
const stageExecPublicationV1 = "exec-publication-v1"

// RetainPublicationLeaseV1 binds only a captured original guardian holder to
// this actual unstarted converter Cmd. This private retained role is NEVER a
// generic FileRule and is never reopened through readonly projection.
func (c *Cmd) RetainPublicationLeaseV1(source *publicationlease.HolderV1) error {
	if c == nil || source == nil {
		return publicationLeaseFailureV1("the original converter publication source is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != phaseNew || c.cmd == nil || c.cmd.Process != nil || c.cmd.Path != self || c.publicationLeaseV1 != nil || c.modeExecV1 != nil || c.slotSourceV1 != nil || c.gvisorOuter != nil {
		return publicationLeaseFailureV1("the original converter cannot acquire this private publication purpose")
	}
	var set setup
	if err := json.Unmarshal(c.setup, &set); err != nil {
		return err
	}
	if err := checkPublicationPurposeV1(&set); err != nil {
		return err
	}
	count := len(c.files) - 2
	code, err := os.Stat(self)
	if err != nil {
		return err
	}
	actual, err := c.files[count].Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(code, actual) {
		return publicationLeaseFailureV1("the converter has another actual helper executable")
	}
	file, record, err := source.RetainForJailV1(c.ctx)
	if err != nil {
		return err
	}
	if err := publicationlease.CheckInheritedV1(file, record); err != nil {
		file.Close()
		return err
	}
	fd := 5 + len(c.files) + len(c.pins)
	set.PublicationLeaseV1 = &publicationLeaseSetupV1{Version: 1, FD: fd, Record: record}
	encoded, err := json.Marshal(set)
	if err != nil {
		file.Close()
		return err
	}
	c.publicationLeaseV1 = &publicationExecLeaseV1{file: file, record: record, fd: fd}
	c.setup = encoded
	c.cmd.Args[1] = publicationFileRulesStageV1(c.cmd.Args[1], &set)
	return nil
}
func checkPublicationPurposeV1(c *setup) error {
	if c == nil || c.Files < 2 || c.Files > 130 || c.Sub != nil || c.GvisorV1Version != 0 || c.ControllerPublicV1 != nil || c.InitLeaseV1Version != 0 || c.VMMExecV1 != nil || c.Net != NetNone || c.Seccomp != ProfileConverter || c.FilesV1Version != 1 || c.BindsV1Version != 0 || len(c.Binds) != 0 || len(c.Rules) != 0 {
		return publicationLeaseFailureV1("the private publication lease has another converter purpose")
	}
	count := c.Files - 2
	program := "/proc/self/fd/" + strconv.Itoa(3+count)
	if c.ExecFD != 3+count || len(c.Argv) < 2 || c.Argv[0] != program {
		return publicationLeaseFailureV1("the converter lost its original helper slot")
	}
	switch c.Argv[1] {
	case "image-convert":
		if len(c.Argv) != 3 || (c.Argv[2] != strconv.Itoa(count) && (count != 0 || c.Argv[2] != "saved-root-v1")) {
			return publicationLeaseFailureV1("the image converter has another closed input vector")
		}
	case "agents-convert":
		if count != 0 || len(c.Argv) != 2 {
			return publicationLeaseFailureV1("the agents converter has another closed input vector")
		}
	default:
		return publicationLeaseFailureV1("the publication fence cannot enter an arbitrary command")
	}
	return nil
}
func (c *Cmd) publicationCapturedFilesV1() ([]*os.File, error) {
	lease := c.publicationLeaseV1
	if lease == nil {
		return nil, nil
	}
	if err := publicationlease.CheckInheritedV1(lease.file, lease.record); err != nil {
		return nil, err
	}
	return []*os.File{lease.file}, nil
}
func (c *Cmd) closePublicationCaptureV1() error {
	lease := c.publicationLeaseV1
	if lease == nil || lease.file == nil {
		return nil
	}
	err := lease.file.Close()
	lease.file = nil
	return err
}
func publicationFileRulesStageV1(stage string, c *setup) string {
	if c.PublicationLeaseV1 == nil {
		return stage
	}
	switch stage {
	case stageInit, stageInitFilesV1:
		return stageInitPublicationV1
	case stageExec, stageExecFilesV1:
		return stageExecPublicationV1
	}
	return stage
}
func checkPublicationFileRulesStageV1(stage string, c *setup) (string, error) {
	normal := ""
	switch stage {
	case stageInitPublicationV1:
		normal = stageInitFilesV1
	case stageExecPublicationV1:
		normal = stageExecFilesV1
	}
	if normal == "" {
		if c.PublicationLeaseV1 != nil {
			return "", publicationLeaseFailureV1("a legacy or foreign stage cannot carry publication lease metadata")
		}
		return stage, nil
	}
	if err := checkPublicationStageV1(c); err != nil {
		return "", err
	}
	return normal, nil
}
func checkPublicationStageV1(c *setup) error {
	if err := checkPublicationPurposeV1(c); err != nil {
		return err
	}
	value := c.PublicationLeaseV1
	if value == nil || value.Version != 1 || value.FD != 5+c.Files+len(c.Binds)+len(pinnedRules(c.Rules)) {
		return publicationLeaseFailureV1("the private publication stage lost its actual retained slot")
	}
	return publicationlease.CheckInheritedFDV1(value.FD, value.Record)
}
func appendPublicationInheritedV1(c *setup, extra []*os.File) []*os.File {
	if c.PublicationLeaseV1 == nil {
		return extra
	}
	return append(extra, os.NewFile(uintptr(c.PublicationLeaseV1.FD), "original-publication-inheritance"))
}

// holdPublicationInitV1 keeps an independent reference to the SAME OFD while
// generic inherited wrappers close before child release. runInit retains this
// exact object through its real reap/endRest join, including startup errors.
func holdPublicationInitV1(c *setup) (*os.File, error) {
	if c.PublicationLeaseV1 == nil {
		return nil, nil
	}
	if err := checkPublicationStageV1(c); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(uintptr(c.PublicationLeaseV1.FD), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "original-publication-init-fence"), nil
}

// retainPublicationExecV1 runs after generic cloexec and final ruleset. Only
// the exact original private slot survives the fixed converter exec. The
// parser cannot unlock/upgrade the shared OFD or mutate locking afterwards.
func retainPublicationExecV1(c *setup) error {
	if c.PublicationLeaseV1 == nil {
		return nil
	}
	if err := checkPublicationStageV1(c); err != nil {
		return err
	}
	// Reserve the fixed raw worker slot without overwriting an unknown file.
	// No os.File/finaliser owns this slot in the converter; only process exit
	// can release it after the filters below are installed.
	fd, err := unix.FcntlInt(uintptr(c.PublicationLeaseV1.FD), unix.F_DUPFD_CLOEXEC, publicationlease.GuardianFDV1)
	if err != nil {
		return err
	}
	if fd != publicationlease.GuardianFDV1 {
		unix.Close(fd)
		return publicationLeaseFailureV1("the fixed converter fence slot is occupied")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		unix.Close(fd)
		return err
	}
	c.Argv[1] += "-publication-v1"
	return protectPublicationFenceLocksV1(fd)
}

// EnterPublicationConverterV1 runs only in the dedicated closed converter
// command before parsing its input. The inherited kernel filter already
// makes the original raw FD undroppable, including across this first exec.
// These checks grant no execution or operation permission.
func EnterPublicationConverterV1() error {
	if err := publicationlease.CheckConverterFDV1(publicationlease.GuardianFDV1); err != nil {
		return err
	}
	return protectPublicationFenceV1(publicationlease.GuardianFDV1, true)
}
func closePublicationInitV1(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
func publicationLeaseFailureV1(message string) error {
	return failure(message, "Retain the original image publication and inspect its native cleanup with host doctor.")
}
