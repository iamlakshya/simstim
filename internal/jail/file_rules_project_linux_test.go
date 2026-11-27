package jail

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// These are ordinary-file namespace sources. They are intentionally separate
// from VMM/KVM, post-exec NoDump and compatibility acceptance.
func TestReadonlyFileProjectionNativeBoundary(t *testing.T) {
	if mode := os.Getenv("JAILTEST_READONLY_PROJECTION_V1"); mode != "" {
		runtime.LockOSThread()
		root, dev := readonlyProjectionRootFixtureV1(t)
		switch mode {
		case "metadata":
			checkReadonlyProjectionMetadataV1(t, root, dev)
		case "prefix":
			checkReadonlyProjectionPrefixV1(t, root, dev)
		case "aliases":
			checkReadonlyProjectionAliasesV1(t, root, dev)
		case "cleanup":
			checkReadonlyProjectionCleanupV1(t, root, dev)
		case "program-base", "program-exec":
			checkReadonlyProjectionProgramV1(t, root, dev, mode == "program-exec")
		case "exec-only-write", "exec-only-read":
			checkReadonlyProjectionExecOnlyV1(t, root, dev, mode == "exec-only-write")
		default:
			t.Fatal("unknown readonly projection fixture role")
		}
		return
	}
	for _, mode := range []string{"metadata", "prefix", "aliases", "cleanup", "program-base", "program-exec", "exec-only-write", "exec-only-read"} {
		t.Run(mode, func(t *testing.T) {
			file := fileRuleFixture(t)
			if _, err := file.Seek(4, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), exe, "-test.run=^TestReadonlyFileProjectionNativeBoundary$")
			command.Env = []string{"JAILTEST_READONLY_PROJECTION_V1=" + mode, "JAILTEST_READONLY_PROJECTION_ROOT=" + t.TempDir()}
			command.ExtraFiles = []*os.File{file}
			command.SysProcAttr = &syscall.SysProcAttr{
				Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
				UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
				GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
				GidMappingsEnableSetgroups: false,
			}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("actual readonly projection namespace fixture failed: %v\n%s", err, output)
			}
			if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != 4 {
				t.Fatalf("projection moved the parent's shared OFD cursor: %d, %v", offset, err)
			}
		})
	}
}

func readonlyProjectionRootFixtureV1(t *testing.T) (int, uint64) {
	t.Helper()
	base := os.Getenv("JAILTEST_READONLY_PROJECTION_ROOT")
	if !filepath.IsAbs(base) || base == "/" {
		t.Fatal("invalid private fixture root")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", base, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"oldroot", "newroot"} {
		if err := os.Mkdir(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Chdir(base); err != nil {
		t.Fatal(err)
	}
	if err := unix.PivotRoot(".", "oldroot"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chdir("/"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", "/newroot", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700"); err != nil {
		t.Fatal(err)
	}
	root, err := unix.Open("/newroot", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(root) })
	var st unix.Stat_t
	if err := unix.Fstat(root, &st); err != nil {
		t.Fatal(err)
	}
	return root, st.Dev
}

func readonlyProjectionRuleFixtureV1(t *testing.T, fd int, role FileRoleV1, mode os.FileMode, access Access) pinnedFileRuleV1 {
	t.Helper()
	rule, err := inspectFileRuleV1(fd, FileRuleV1{FD: fd, Role: role, Mode: mode, Access: access})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func checkReadonlyProjectionMetadataV1(t *testing.T, root int, dev uint64) {
	t.Helper()
	rule := readonlyProjectionRuleFixtureV1(t, 3, FileDataV1, 0o600, Read)
	before, err := unix.FcntlInt(3, unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectReadonlyFileRulesV1(root, dev, []pinnedFileRuleV1{rule}); err != nil {
		t.Fatal(err)
	}
	if err := verifyReadonlyFileV1(3, readonlyFileSnapshotV1{rule: rule}); err != nil {
		t.Fatal(err)
	}
	after, err := unix.FcntlInt(3, unix.F_GETFD, 0)
	if err != nil || after != before {
		t.Fatalf("projection changed FD_CLOEXEC: %d -> %d: %v", before, after, err)
	}
	if offset, err := unix.Seek(3, 0, unix.SEEK_CUR); err != nil || offset != 4 {
		t.Fatalf("the initial child cursor changed: %d %v", offset, err)
	}
	for operation, err := range map[string]error{
		"chmod":      unix.Fchmod(3, 0o644),
		"chown":      unix.Fchown(3, -1, -1),
		"timestamps": unix.Futimes(3, []unix.Timeval{{Sec: 1}, {Sec: 1}}),
	} {
		if !errors.Is(err, unix.EROFS) {
			t.Fatalf("read-only projection allowed metadata operation %s: %v", operation, err)
		}
	}
	writer, err := unix.Open(mountFDPath(3), unix.O_RDWR|unix.O_CLOEXEC, 0)
	if writer >= 0 {
		_ = unix.Close(writer)
	}
	if !errors.Is(err, unix.EROFS) {
		t.Fatalf("self-proc reopened a writable descriptor from the RO mount: %v", err)
	}
	data := make([]byte, 4)
	if n, err := unix.Read(3, data); err != nil || n != 4 || string(data) != "cted" {
		t.Fatalf("projection changed input bytes or cursor: %q %v", data, err)
	}
}

func checkReadonlyProjectionPrefixV1(t *testing.T, root int, dev uint64) {
	t.Helper()
	if err := unix.Mkdirat(root, readonlyFilesPrefixV1, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := unix.Openat(root, readonlyFilesPrefixV1+"/keep", unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(marker)
	rule := readonlyProjectionRuleFixtureV1(t, 3, FileDataV1, 0o600, Read)
	if err := projectReadonlyFileRulesV1(root, dev, []pinnedFileRuleV1{rule}); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("a conflicting private prefix was accepted: %v", err)
	}
	marker, err = unix.Openat(root, readonlyFilesPrefixV1+"/keep", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal("failure removed an existing prefix", err)
	}
	_ = unix.Close(marker)
	flags, err := unix.FcntlInt(3, unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDWR {
		t.Fatal("a prefix refusal replaced the original descriptor", err)
	}
}

func checkReadonlyProjectionAliasesV1(t *testing.T, root int, dev uint64) {
	t.Helper()
	alias, err := unix.FcntlInt(3, unix.F_DUPFD_CLOEXEC, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(alias)
	independent, err := unix.Open(mountFDPath(3), unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(independent)
	if _, err := unix.Seek(independent, 4, unix.SEEK_SET); err != nil {
		t.Fatal(err)
	}
	var rules []pinnedFileRuleV1
	for _, fd := range []int{3, alias, independent} {
		rules = append(rules, readonlyProjectionRuleFixtureV1(t, fd, FileDataV1, 0o600, Read))
	}
	_, compareErr := sameReadonlyOFDV1(3, alias)
	if compareErr != nil {
		if err := projectReadonlyFileRulesV1(root, dev, rules); err == nil || !strings.Contains(err.Error(), "disambiguate same-inode OFDs") {
			t.Fatalf("unavailable KCMP_FILE silently merged or split aliases: %v", err)
		}
		if _, err := unix.Openat(root, readonlyFilesPrefixV1, unix.O_PATH|unix.O_CLOEXEC, 0); !errors.Is(err, unix.ENOENT) {
			t.Fatal("ambiguous alias grouping made filesystem effects", err)
		}
		return
	}
	if err := projectReadonlyFileRulesV1(root, dev, rules); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Seek(3, 8, unix.SEEK_SET); err != nil {
		t.Fatal(err)
	}
	sharedOffset, sharedErr := unix.Seek(alias, 0, unix.SEEK_CUR)
	otherOffset, otherErr := unix.Seek(independent, 0, unix.SEEK_CUR)
	if sharedErr != nil || otherErr != nil || sharedOffset != 8 || otherOffset != 4 {
		t.Fatalf("OFD alias topology changed: shared=%d independent=%d errors=%v/%v", sharedOffset, otherOffset, sharedErr, otherErr)
	}
	for _, fd := range []int{alias, independent} {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Fatalf("a close-on-exec alias lost its descriptor flag: %d %v", fd, err)
		}
	}
}

func projectionInodeFDCountV1(t *testing.T, rule pinnedFileRuleV1) int {
	t.Helper()
	entries, err := os.ReadDir("/oldroot/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var st unix.Stat_t
		if unix.Fstat(fd, &st) == nil && st.Dev == rule.Dev && st.Ino == rule.Ino {
			count++
		}
	}
	return count
}

func checkReadonlyProjectionCleanupV1(t *testing.T, root int, dev uint64) {
	t.Helper()
	bad, err := unix.Open("/unsealed-program", unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(bad)
	if err := unix.Fchmod(bad, 0o111); err != nil {
		t.Fatal(err)
	}
	data := readonlyProjectionRuleFixtureV1(t, 3, FileDataV1, 0o600, Read)
	program := readonlyProjectionRuleFixtureV1(t, bad, FileProgramV1, 0o111, Read|Exec)
	before := projectionInodeFDCountV1(t, data)
	if err := projectReadonlyFileRulesV1(root, dev, []pinnedFileRuleV1{data, program}); err == nil {
		t.Fatal("an unsealed host-style program entered the temporary chmod path")
	}
	if after := projectionInodeFDCountV1(t, data); after != before {
		t.Fatalf("a failed later group leaked a reopened earlier OFD: %d -> %d", before, after)
	}
	if fd, err := unix.Openat(root, readonlyFilesPrefixV1, unix.O_PATH|unix.O_CLOEXEC, 0); !errors.Is(err, unix.ENOENT) {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		t.Fatal("failed projection left its private mount prefix", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(bad, &st); err != nil || st.Mode&0o7777 != 0o111 {
		t.Fatal("failure changed an unsealed regular program's mode", err)
	}
	flags, err := unix.FcntlInt(3, unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDWR {
		t.Fatal("preparation failure replaced an original slot", err)
	}
}

func checkReadonlyProjectionProgramV1(t *testing.T, root int, dev uint64, execSeal bool) {
	t.Helper()
	fd, err := unix.MemfdCreate("readonly-program-fixture", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := unix.Write(fd, []byte("ordinary program fixture")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fchmod(fd, 0o111); err != nil {
		t.Fatal(err)
	}
	seals := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if execSeal {
		seals |= unix.F_SEAL_EXEC
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, seals); err != nil {
		t.Fatal(err)
	}
	rule := readonlyProjectionRuleFixtureV1(t, fd, FileProgramV1, 0o111, Read|Exec)
	if err := projectReadonlyFileRulesV1(root, dev, []pinnedFileRuleV1{rule}); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&0o7777 != 0o111 {
		t.Fatalf("the unreadable executable mode was not restored: %#o %v", st.Mode, err)
	}
	if err := unix.Fchmod(fd, 0o511); !errors.Is(err, unix.EROFS) {
		t.Fatalf("a sealed program bypassed read-only metadata projection: %v", err)
	}
	if err := verifyReadonlyFileV1(fd, readonlyFileSnapshotV1{rule: rule}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8)
	if _, err := unix.Pread(fd, data, 0); err != nil || string(data) != "ordinary" {
		t.Fatalf("the reopened readable program descriptor changed: %q %v", data, err)
	}
	fmt.Fprintln(os.Stdout, "program-mode-restored")
}

func checkReadonlyProjectionExecOnlyV1(t *testing.T, root int, dev uint64, writeOnly bool) {
	t.Helper()
	// An ordinary linked file deliberately lacks memfd seals. An Exec-only
	// projection needs no temporary read permission and must leave mode 0111.
	const name = "/exec-only-regular-program"
	fd, err := unix.Open(name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Write(fd, []byte("ordinary executable fixture")); err != nil {
		t.Fatal(err)
	}
	if !writeOnly {
		readable, err := unix.Open(name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		_ = unix.Close(fd)
		fd = readable
	}
	defer unix.Close(fd)
	if err := unix.Fchmod(fd, 0o111); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Seek(fd, 3, unix.SEEK_SET); err != nil {
		t.Fatal(err)
	}
	rule := readonlyProjectionRuleFixtureV1(t, fd, FileProgramV1, 0o111, Exec)
	before, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectReadonlyFileRulesV1(root, dev, []pinnedFileRuleV1{rule}); err != nil {
		t.Fatal(err)
	}
	if err := verifyReadonlyFileV1(fd, readonlyFileSnapshotV1{rule: rule}); err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_PATH == 0 {
		t.Fatalf("Exec-only projection gained an ordinary readable descriptor: %#x %v", flags, err)
	}
	after, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || after != before {
		t.Fatalf("Exec-only projection changed FD_CLOEXEC: %d -> %d: %v", before, after, err)
	}
	if _, err := unix.Read(fd, make([]byte, 1)); !errors.Is(err, unix.EBADF) {
		t.Fatalf("Exec-only projection acquired read authority: %v", err)
	}
	if _, err := unix.Seek(fd, 0, unix.SEEK_CUR); !errors.Is(err, unix.EBADF) {
		t.Fatalf("Exec-only O_PATH unexpectedly exposed a cursor: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Dev != rule.Dev || st.Ino != rule.Ino || st.Mode&0o7777 != 0o111 {
		t.Fatalf("Exec-only projection changed inode or unreadable mode: %+v %v", st, err)
	}
	if err := dropCaps(); err != nil {
		t.Fatal(err)
	}
	reopened, err := unix.Open(mountFDPath(fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if reopened >= 0 {
		_ = unix.Close(reopened)
	}
	if !errors.Is(err, unix.EACCES) {
		t.Fatalf("the unchanged execute-only inode gained read access through self-proc: %v", err)
	}
}
