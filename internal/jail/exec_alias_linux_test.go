package jail

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExposureNamesKeepAllMatchingPhysicalAncestors(t *testing.T) {
	file, bound, other := devIno{1, 10}, devIno{1, 20}, devIno{1, 30}
	names, err := exposureNames(bind{Path: "/view", Dev: bound.dev, Ino: bound.ino},
		"/host/alias/sub/run", []devIno{file, bound, other, bound})
	if err != nil || !slices.Equal(names, []string{"/view/run", "/view/alias/sub/run"}) {
		t.Fatalf("physical aliases = %q, %v", names, err)
	}
	names, err = exposureNames(bind{Path: "/view", Dev: 2, Ino: 1}, "/host/run", []devIno{file, bound})
	if err != nil || len(names) != 0 {
		t.Fatalf("an unrelated bind gained an exposure: %q, %v", names, err)
	}
	_, err = exposureNames(bind{Path: "/view", Dev: bound.dev, Ino: bound.ino},
		"/run", []devIno{file, other, bound})
	if err == nil || hintOf(err) == "" {
		t.Fatalf("an unrepresentable physical alias needs a clear hint: %v", err)
	}
}

func TestExplicitExecNeedsBothPathAndPhysicalCoverage(t *testing.T) {
	file, sub, outside, tree := devIno{1, 10}, devIno{1, 20}, devIno{1, 30}, devIno{1, 40}
	rules := []rule{
		{Path: "/deep/tree", Access: Read | Exec, Dir: true, FD: 3, Dev: tree.dev, Ino: tree.ino},
		{Path: "/physical/outside/sub/run", Access: Read | Exec, FD: 4, Dev: file.dev, Ino: file.ino},
	}
	outsideLine := []devIno{file, sub, outside}
	if explicitExec("/deep/tree/child/run", outsideLine, rules) {
		t.Error("a lexical directory prefix made a different physical tree executable")
	}
	if !explicitExec("/physical/outside/sub/run", outsideLine, rules) {
		t.Error("the exact explicit file exception was lost")
	}
	if !explicitExec("/deep/tree/child/run", []devIno{file, sub, tree}, rules) {
		t.Error("a directory rule with both forms of coverage was lost")
	}
	if explicitExec("/elsewhere/run", []devIno{file, sub, tree}, rules) {
		t.Error("physical membership alone made a different path executable")
	}
	writeOnly := []rule{{Path: "/deep/tree/child/run", Access: Read | Write, FD: 5, Dev: file.dev, Ino: file.ino}}
	if explicitExec("/deep/tree/child/run", outsideLine, writeOnly) {
		t.Error("a rule without Exec made an alias executable")
	}
}

func TestExecutableAccessKeepsChildWriteExceptions(t *testing.T) {
	parent, child, file := devIno{1, 10}, devIno{1, 20}, devIno{1, 30}
	rules := []rule{
		{Path: "/parent", Access: Read | Exec, Dir: true, FD: 3, Dev: parent.dev, Ino: parent.ino},
		{Path: "/parent/child", Access: Read | Write | Exec, Dir: true, FD: 4, Dev: child.dev, Ino: child.ino},
	}
	access, err := executableAccess([]devIno{file, child, parent}, rules)
	if err != nil || access&Write == 0 || access&Exec == 0 {
		t.Fatalf("the nearest child exception lost Write or Exec: %v, %v", access, err)
	}
	access, err = executableAccess([]devIno{parent}, rules)
	if err != nil || access&Write != 0 || access&Exec == 0 {
		t.Fatalf("the parent stopped being read-only executable: %v, %v", access, err)
	}
	rules = append(rules, rule{Path: "/other-child", Access: Read | Exec, Dir: true,
		FD: 5, Dev: child.dev, Ino: child.ino})
	if _, err := executableAccess([]devIno{file, child, parent}, rules); err == nil || hintOf(err) == "" {
		t.Fatalf("conflicting aliases of one executable object need a clear hint: %v", err)
	}
}

func aliasTestSource(t *testing.T, name string) (int, unix.Stat_t, string) {
	t.Helper()
	fd, err := unix.Open(name, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		t.Fatal(err)
	}
	return fd, st, real
}

func aliasTestCopy(t *testing.T, fd int) int {
	t.Helper()
	copy, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		t.Fatal(err)
	}
	return copy
}

func TestExecMountListKeepsDescriptorsAlignedWhenCoalescing(t *testing.T) {
	name := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(name, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, st, real := aliasTestSource(t, name)
	var list execMountList
	t.Cleanup(list.close)
	r := execMount{rule: rule{Path: "/view/run", Real: real, Access: Read | Exec, Dev: st.Dev, Ino: st.Ino}, noexec: true}
	if err := list.add(r, aliasTestCopy(t, fd)); err != nil {
		t.Fatal(err)
	}
	r.noexec = false
	if err := list.add(r, aliasTestCopy(t, fd)); err != nil {
		t.Fatal(err)
	}
	r.noexec = true
	if err := list.add(r, aliasTestCopy(t, fd)); err != nil {
		t.Fatal(err)
	}
	if len(list.mounts) != 1 || len(list.fds) != 1 || list.mounts[0].noexec {
		t.Fatalf("coalesced mount and descriptor arrays disagree: mounts=%+v, fds=%v", list.mounts, list.fds)
	}
	var kept unix.Stat_t
	if err := unix.Fstat(list.fds[0], &kept); err != nil || kept.Dev != st.Dev || kept.Ino != st.Ino {
		t.Fatalf("the retained descriptor lost its object: %v, %+v", err, kept)
	}
	r.Access |= Write
	if err := list.add(r, aliasTestCopy(t, fd)); err == nil || hintOf(err) == "" {
		t.Fatalf("a conflicting Write permission needs a hint: %v", err)
	}
	if len(list.mounts) != 1 || len(list.fds) != 1 || list.mounts[0].Access&Write != 0 {
		t.Fatal("a rejected duplicate changed the retained permission or descriptor arrays")
	}
}

func TestExecMountAliasesVerifyTheOpenedObject(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "other"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fd, st, real := aliasTestSource(t, filepath.Join(root, "first"))
	other, _, _ := aliasTestSource(t, filepath.Join(root, "other"))
	r := rule{Path: "/declared/run", Real: real, Access: Read | Exec, FD: fd, Dev: st.Dev, Ino: st.Ino}
	var list execMountList
	t.Cleanup(list.close)
	if err := list.add(execMount{rule: r}, aliasTestCopy(t, fd)); err != nil {
		t.Fatal(err)
	}
	if err := list.addAlias(bind{Path: "/alias"}, other, r, "/alias", r.Access, []rule{r}); err == nil || hintOf(err) == "" {
		t.Fatalf("a different alias object needs a clear failure: %v", err)
	}
	if len(list.mounts) != 1 || len(list.fds) != 1 || list.mounts[0].Path != r.Path {
		t.Fatal("a rejected alias changed the declared capability or descriptor arrays")
	}
	var stillOpen unix.Stat_t
	if err := unix.Fstat(other, &stillOpen); err != nil {
		t.Fatalf("alias validation closed the borrowed bind descriptor: %v", err)
	}
}

func TestExecMountsRetainTheDeclaredPathAcrossASelfBind(t *testing.T) {
	const marker = "JAILTEST_EXEC_ALIAS_SELF_BIND"
	if os.Getenv(marker) != "1" {
		inMode(t, testMode{"namespaces", true})
		cmd := exec.CommandContext(t.Context(), testExe(t), "-test.run=^TestExecMountsRetainTheDeclaredPathAcrossASelfBind$")
		cmd.Env = append(os.Environ(), marker+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
			GidMappingsEnableSetgroups: false,
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the private namespace alias fixture: %v\n%s", err, out)
		}
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	bound := filepath.Join(t.TempDir(), "bound")
	if err := os.MkdirAll(bound+"/sub", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bound+"/run", []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(bound, bound+"/sub", "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Unmount(bound+"/sub", unix.MNT_DETACH) }()
	bindFD, bindStat, bindReal := aliasTestSource(t, bound)
	declared := bound + "/sub/run"
	ruleFD, ruleStat, ruleReal := aliasTestSource(t, declared)
	c := &setup{
		Binds: []bind{{Path: bound, Real: bindReal, Write: true, Kind: kindDir, FD: bindFD, Dev: bindStat.Dev, Ino: bindStat.Ino}},
		Rules: []rule{{Path: declared, Real: ruleReal, Access: Read | Exec, FD: ruleFD, Dev: ruleStat.Dev, Ino: ruleStat.Ino}},
	}
	before := slices.Clone(c.Rules)
	mounts, fds, err := execMounts(c, []int{bindFD})
	for _, fd := range fds {
		defer func() { _ = unix.Close(fd) }()
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != len(fds) || !slices.Equal(before, c.Rules) {
		t.Fatal("alias planning changed the explicit rules or misaligned descriptors")
	}
	found := false
	for i, mount := range mounts {
		if mount.Path == declared {
			found = true
			if mount.noexec || mount.Access&Write != 0 {
				t.Fatalf("the declared read-only Exec exception changed: %+v", mount)
			}
		}
		var st unix.Stat_t
		if err := unix.Fstat(fds[i], &st); err != nil || st.Dev != mount.Dev || st.Ino != mount.Ino {
			t.Fatalf("an executable mount lost descriptor identity: %v, %+v", err, mount)
		}
	}
	if !found {
		t.Fatalf("the declared exception %s was omitted: %+v", declared, mounts)
	}
}

func TestNamespaceRelativeAliasesNeedTheirOwnExecCoverage(t *testing.T) {
	inMode(t, testMode{"namespaces", true})
	root := hiddenPath(t.TempDir())
	physical, view := root+"/physical", root+"/view"
	for _, dir := range []string{physical + "/A", physical + "/outside/sub", view} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tree := view + "/tree"
	if err := os.Symlink(physical+"/A", tree); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside/sub", physical+"/A/child"); err != nil {
		t.Fatal(err)
	}
	primary, alias := physical+"/outside/sub/run", tree+"/child/run"
	if err := os.WriteFile(primary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(primary, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{primary, alias} {
		control := exec.CommandContext(t.Context(), name)
		control.Env = probeEnv
		if err := control.Run(); err != nil {
			t.Fatalf("the unjailed control cannot execute %s: %v", name, err)
		}
	}
	script := `"$1" && printf 'PRIMARY_OK\n'
if "$2"; then printf 'ALIAS_RAN\n'; else printf 'ALIAS_BLOCKED\n'; fi
if printf 'changed' >"$2"; then printf 'ALIAS_WRITABLE\n'; else printf 'ALIAS_READONLY\n'; fi`
	c, err := Command(t.Context(), Spec{Argv: []string{"/bin/sh", "-c", script, "sh", primary, alias},
		Binds: append(libBinds(), Bind{Path: tree, Write: true}, Bind{Path: tree + "/child", Write: true}, Bind{Path: physical + "/outside", Write: true}),
		Rules: []PathRule{{Path: tree, Access: Read | Exec}, {Path: primary, Access: Read | Exec}}, Seccomp: ProfileVMM})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PRIMARY_OK\n") || !strings.Contains(string(out), "ALIAS_BLOCKED\n") ||
		!strings.Contains(string(out), "ALIAS_READONLY\n") || strings.Contains(string(out), "ALIAS_RAN\n") || strings.Contains(string(out), "ALIAS_WRITABLE\n") {
		t.Fatalf("a relative alias gained executable or writable access: %v\n%s", err, out)
	}
}
