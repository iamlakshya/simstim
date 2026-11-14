package jail

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func fileRuleFixture(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "selected")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if _, err := file.WriteString("selected-data"); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestFileRuleSnapshotsPreserveOffsetAndOFDFlags(t *testing.T) {
	file := fileRuleFixture(t)
	if _, err := file.Seek(7, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := raw.Control(func(fd uintptr) { before, err = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	rules, err := snapshotFileRulesV1([]FileRuleV1{{FD: 3, Role: FileDataV1, Mode: 0o600, Access: Read | Write}}, []*os.File{file})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCapturedFileRulesV1(rules, []*os.File{file}); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := raw.Control(func(fd uintptr) { after, err = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil {
		t.Fatal(err)
	}
	if err != nil || after != before {
		t.Fatal("capture mutated shared OFD flags", before, after, err)
	}
	if offset, err := file.Seek(0, 1); err != nil || offset != 7 {
		t.Fatal("capture mutated shared file offset", offset, err)
	}
}

func TestFileRulesRefuseSpecialObjectsAndDescriptorAccessEscalation(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	device, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for _, file := range []*os.File{dir, device, reader, writer} {
		if _, err := snapshotFileRulesV1([]FileRuleV1{{FD: 3, Role: FileDataV1, Mode: 0o600, Access: Read}}, []*os.File{file}); err == nil {
			t.Fatal("nonregular object received an inode grant")
		}
	}
	file := fileRuleFixture(t)
	readonly, err := os.Open(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, err := snapshotFileRulesV1([]FileRuleV1{{FD: 3, Role: FileDataV1, Mode: 0o600, Access: Write}}, []*os.File{readonly}); err == nil {
		t.Fatal("read-only descriptor became a write capability")
	}
}

func TestFileRulesUseFinalCapturedSlotsAndRejectAliases(t *testing.T) {
	file := fileRuleFixture(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	c := lifecycleCmd(t)
	c.files = []*os.File{writer, file}
	rules, err := snapshotFileRulesV1([]FileRuleV1{{FD: 4, Role: FileDataV1, Mode: 0o600, Access: Read}}, c.files)
	if err != nil {
		t.Fatal(err)
	}
	final, release, err := c.stdio()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := checkCapturedFileRulesV1(rules, final); err != nil {
		t.Fatal(err)
	}
	for _, text := range [][]FileRuleV1{{{FD: 3, Role: FileDataV1, Mode: 0o600, Access: Read}}, {{FD: 3, Role: FileDataV1, Mode: 0o600, Access: Read}, {FD: 4, Role: FileDataV1, Mode: 0o600, Access: Write}}} {
		aliases := []*os.File{file, file}
		pinned, err := snapshotFileRulesV1(text, aliases)
		if err != nil {
			t.Fatal(err)
		}
		if checkCapturedFileRulesV1(pinned, aliases) == nil {
			t.Fatal("an inode alias widened a selected slot's rights")
		}
	}
}

func TestFileRulesRejectReplacementBeforeAnyStageStarts(t *testing.T) {
	file, other := fileRuleFixture(t), fileRuleFixture(t)
	c := lifecycleCmd(t)
	c.files = []*os.File{file}
	var err error
	c.fileRulesV1, err = snapshotFileRulesV1([]FileRuleV1{{FD: 3, Role: FileDataV1, Mode: 0o600, Access: Read}}, c.files)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := file.SyscallConn()
	replacement, _ := other.SyscallConn()
	var replaceErr error
	if err := raw.Control(func(fd uintptr) {
		controlErr := replacement.Control(func(next uintptr) { replaceErr = unix.Dup3(int(next), int(fd), unix.O_CLOEXEC) })
		if controlErr != nil {
			replaceErr = controlErr
		}
	}); err != nil {
		t.Fatal(err)
	}
	if replaceErr != nil {
		t.Fatal(replaceErr)
	}
	if err := c.Start(); err == nil {
		c.killGroup()
		_ = c.Wait()
		t.Fatal("descriptor replacement reached a stage")
	}
	if c.cmd.Process != nil || c.Pid() != 0 {
		t.Fatal("a stage started before identity refusal")
	}
}

func TestFileRulesVersionedStagesAndStrictSetup(t *testing.T) {
	for _, stage := range []string{stageOuter, stageInit, stageExec} {
		set := &setup{FilesV1Version: 1, Files: 1, FileRulesV1: []pinnedFileRuleV1{{FileRuleV1: FileRuleV1{FD: 3, Role: FileDataV1, Mode: 0o600, Access: Read}}}}
		wire := fileRulesStageV1(stage, set)
		if wire == stage {
			t.Fatal("old helper could silently ignore file grants")
		}
		if got, err := checkFileRulesStageV1(wire, set); err != nil || got != stage {
			t.Fatal(got, err)
		}
		if _, err := checkFileRulesStageV1(stage, set); err == nil {
			t.Fatal("legacy stage accepted new security fields")
		}
		set.FilesV1Version = 2
		if _, err := checkFileRulesStageV1(wire, set); err == nil {
			t.Fatal("unknown grant version accepted")
		}
	}
	for _, body := range []string{`{"argv":["/bin/true"],"files":0,"unknown_security_field":1}`, `{"argv":["/bin/true"],"files":0} {}`} {
		file, err := os.Create(filepath.Join(t.TempDir(), "setup"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(body); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := readSetup(strconv.Itoa(int(file.Fd()))); err == nil {
			t.Fatal("unknown or trailing setup content accepted")
		}
		file.Close()
	}
}
