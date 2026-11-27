package jail

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBackingMountsKeepStackedRootsAndEscapedPaths(t *testing.T) {
	text := "1 0 8:1 / / rw - ext4 none rw\n" +
		`2 1 8:1 /source\040tree /alias rw shared:2 - ext4 none rw` + "\n" +
		`3 1 8:1 /other\134040 /alias rw - ext4 none rw` + "\n" +
		`4 1 9:1 /wrong /alias rw - ext4 none rw` + "\n"
	mounts, err := parseBackingMounts(text)
	if err != nil || len(mounts) != 4 {
		t.Fatalf("stacked mount records: %+v, %v", mounts, err)
	}
	paths, err := backingPaths("/alias/run", unix.Mkdev(8, 1), mounts)
	want := []string{"/alias/run", "/source tree/run", "/other\\040/run"}
	if err != nil || !slices.Equal(paths, want) {
		t.Fatalf("all possible roots = %q, %v, want %q", paths, err, want)
	}
	decoded, err := backingPath(`/a\011b\012c\134d\377`)
	if err != nil || decoded != "/a\tb\nc\\d\xff" {
		t.Fatalf("octal path = %q, %v", decoded, err)
	}
	long := "1 0 8:1 / / rw future:" + strings.Repeat("x", 70*1024) + " - ext4 none rw\n"
	if _, err := parseBackingMounts(long); err != nil {
		t.Fatalf("a record longer than a scanner token was refused: %v", err)
	}
}

func TestBackingMountsRejectMalformedOrUnavailableTopology(t *testing.T) {
	for _, text := range []string{
		"", "1 0 8:1 / / rw - ext4 none\n", "x 0 8:1 / / rw - ext4 none rw\n",
		"1 0 8:x / / rw - ext4 none rw\n", "1 0 4294967296:1 / / rw - ext4 none rw\n",
		"1 0 8:1 / / ro,rw - ext4 none rw\n", `1 0 8:1 /bad\04 / rw - ext4 none rw`,
		`1 0 8:1 /bad\08x / rw - ext4 none rw`, `1 0 8:1 /bad\000 / rw - ext4 none rw`,
		`1 0 8:1 /bad\400 / rw - ext4 none rw`, "1 0 8:1 relative / rw - ext4 none rw\n",
		"1 0 8:1 / /a/../b rw - ext4 none rw\n",
	} {
		if _, err := parseBackingMounts(text); err == nil || hintOf(err) == "" {
			t.Errorf("malformed topology needs a clear hint: %q: %v", text, err)
		}
	}
	mounts, err := parseBackingMounts("1 0 8:1 / / rw - ext4 none rw\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backingPaths("/source", unix.Mkdev(9, 1), mounts); err == nil || hintOf(err) == "" {
		t.Fatalf("missing same-device records need a clear hint: %v", err)
	}
}

func TestBackingRelationsIncludeChildrenAndHigherRoots(t *testing.T) {
	mounts, err := parseBackingMounts("1 0 8:1 / / rw - ext4 none rw\n" +
		"2 1 8:1 /source/sub /alias rw - ext4 none rw\n" +
		"3 1 8:1 /disjoint /alias rw - ext4 none rw\n" +
		"4 1 8:1 /source/sub/run /file-view rw - ext4 none rw\n" +
		"5 1 8:1 /source /tree-view rw - ext4 none rw\n")
	if err != nil {
		t.Fatal(err)
	}
	source := &execSource{rule: rule{Path: "/source", Real: "/source", Dev: unix.Mkdev(8, 1), Dir: true},
		locations: []execLocation{{real: "/source"}}}
	relations, err := possibleBackingRelations(source, bind{Path: "/alias", Real: "/alias", Dev: source.Dev}, mounts)
	if err != nil || !slices.Contains(relations.children, "sub") {
		t.Fatalf("a stacked record's possible child was lost: %+v, %v", relations, err)
	}
	source = &execSource{rule: rule{Path: "/file-view", Real: "/file-view", Dev: unix.Mkdev(8, 1)},
		locations: []execLocation{{real: "/file-view"}}}
	relations, err = possibleBackingRelations(source, bind{Path: "/tree-view", Real: "/tree-view", Dev: source.Dev}, mounts)
	if err != nil || !slices.Contains(relations.aliases, "/tree-view/sub/run") {
		t.Fatalf("a higher writable root's file exposure was lost: %+v, %v", relations, err)
	}
}

func backingTestSource(t *testing.T, name string) *execSource {
	t.Helper()
	fd, st, real := aliasTestSource(t, name)
	line := []devIno{{st.Dev, st.Ino}}
	source := &execSource{rule: rule{Path: "/view", Real: real, Dir: st.Mode&unix.S_IFMT == unix.S_IFDIR,
		Access: Read | Exec, FD: fd, Dev: st.Dev, Ino: st.Ino}, fd: fd, origin: line}
	source.addLocation(real, line)
	return source
}

func backingTestBind(t *testing.T, name string) (bind, int) {
	t.Helper()
	fd, st, real := aliasTestSource(t, name)
	return bind{Path: name, Real: real, Kind: kindOf(st.Mode), Write: true, FD: fd, Dev: st.Dev, Ino: st.Ino}, fd
}

func TestBackingChildrenNeedPositiveDescriptorAndOriginProof(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Mkdir(root+"/child", 0o700); err != nil {
		t.Fatal(err)
	}
	source := backingTestSource(t, root)
	bound, _ := backingTestBind(t, root+"/child")
	child, err := verifiedBackingChild(source, bound, "child", []rule{source.rule})
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(child.fd)
	if child.Dev != bound.Dev || child.Ino != bound.Ino || child.Access&Write != 0 || child.Access&Exec == 0 ||
		!slices.Contains(child.origin, devIno{source.Dev, source.Ino}) {
		t.Fatalf("the verified child lost its object or inherited origin: %+v", child)
	}
	outside := hiddenPath(t.TempDir())
	other, _ := backingTestBind(t, outside)
	for _, relative := range []string{"child", "../outside"} {
		if child, err := verifiedBackingChild(source, other, relative, []rule{source.rule}); err == nil || hintOf(err) == "" {
			if child != nil {
				_ = unix.Close(child.fd)
			}
			t.Errorf("unverified candidate %s did not fail clearly: %v", relative, err)
		}
	}
	if err := os.Symlink(outside, root+"/link"); err != nil {
		t.Fatal(err)
	}
	if child, err := verifiedBackingChild(source, other, "link", []rule{source.rule}); err == nil || hintOf(err) == "" {
		if child != nil {
			_ = unix.Close(child.fd)
		}
		t.Errorf("a symlink was treated as proven physical containment: %v", err)
	}
}

func TestPossibleBackingOverlapCannotUseTextAsAuthority(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Mkdir(root+"/child", 0o700); err != nil {
		t.Fatal(err)
	}
	source := backingTestSource(t, root)
	other, otherFD := backingTestBind(t, hiddenPath(t.TempDir()))
	device := fmt.Sprintf("%d:%d", unix.Major(source.Dev), unix.Minor(source.Dev))
	text := "1 0 " + device + " / / rw - fixture none rw\n" +
		"2 1 " + device + " " + backingTestPath(root+"/child") + " " + backingTestPath(other.Real) + " rw - fixture none rw\n"
	mounts, err := parseBackingMounts(text)
	if err != nil {
		t.Fatal(err)
	}
	var list execMountList
	list.backing, list.backingRead = mounts, true
	t.Cleanup(list.close)
	if err := list.protectBind(source, other, otherFD, []rule{source.rule}); err == nil || hintOf(err) == "" {
		t.Fatalf("an unverified topology overlap did not fail clearly: %v", err)
	}
	if len(list.work) != 0 || len(list.mounts) != 0 {
		t.Fatal("mount text alone created a protected capability")
	}
}

func backingTestPath(name string) string {
	return strings.NewReplacer("\\", `\134`, " ", `\040`, "\t", `\011`, "\n", `\012`).Replace(name)
}

func TestWritableOnlyAliasCannotLoseItsRequestedWrite(t *testing.T) {
	object := devIno{1, 10}
	rules := []rule{{Path: "/exec", Access: Read | Exec, FD: 3, Dev: object.dev, Ino: object.ino},
		{Path: "/write-alias", Access: Read | Write, FD: 4, Dev: object.dev, Ino: object.ino}}
	if _, err := executableAccess([]devIno{object}, rules); err == nil || hintOf(err) == "" {
		t.Fatalf("a writable-only alias was silently made read-only: %v", err)
	}
	// check merges rights on one path before pinning, so the same-path
	// union is an explicit writable executable exception.
	rules = []rule{{Path: "/exec", Access: Read | Write | Exec, FD: 3, Dev: object.dev, Ino: object.ino}}
	if access, err := executableAccess([]devIno{object}, rules); err != nil || access&Write == 0 {
		t.Fatalf("a merged same-path exception was refused: %v, %v", access, err)
	}
}

func TestProtectedSourceWorklistCoalescesPhysicalObjects(t *testing.T) {
	fixture := backingTestSource(t, hiddenPath(t.TempDir()))
	var list execMountList
	t.Cleanup(list.close)
	makeSource := func(access Access) *execSource {
		source := &execSource{rule: fixture.rule, fd: aliasTestCopy(t, fixture.fd), origin: slices.Clone(fixture.origin)}
		source.Access = access
		source.addLocation(fixture.Real, source.origin)
		return source
	}
	first := makeSource(Read | Exec)
	if err := list.rememberSource(first); err != nil {
		t.Fatal(err)
	}
	duplicate := makeSource(Read | Exec)
	if err := list.rememberSource(duplicate); err != nil {
		t.Fatal(err)
	}
	if len(list.work) != 1 || duplicate.fd != -1 {
		t.Fatal("one protected object entered the worklist twice or retained an extra descriptor")
	}
	conflict := makeSource(Read | Write | Exec)
	if err := list.rememberSource(conflict); err == nil || hintOf(err) == "" || conflict.fd != -1 {
		t.Fatalf("conflicting global protection did not fail clearly: %v", err)
	}
	if len(list.work) != 1 || first.Access&Write != 0 {
		t.Fatal("a rejected protection changed the established source")
	}
	var st unix.Stat_t
	if err := unix.Fstat(fixture.fd, &st); err != nil {
		t.Fatalf("worklist ownership closed the borrowed fixture descriptor: %v", err)
	}
}

func TestEveryNamedOriginMustBeResolvedBeforeSkippingTopology(t *testing.T) {
	root := hiddenPath(t.TempDir())
	name := root + "/run"
	if err := os.WriteFile(name, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, st, real := aliasTestSource(t, name)
	bound, boundFD := backingTestBind(t, root)
	source := &execSource{rule: rule{Path: name, Real: real, Access: Read | Exec, FD: fd, Dev: st.Dev, Ino: st.Ino},
		fd: fd, origin: []devIno{{st.Dev, st.Ino}, {bound.Dev, bound.Ino}}}
	source.addLocation(real, source.origin)
	// A second explicit name can arrive through a mounted file root whose
	// parent walk does not contain this writable bind. Its topology still
	// needs checking, even though the first name is already protected.
	source.addLocation("/mounted-name", []devIno{{st.Dev, st.Ino}})
	var list execMountList
	t.Cleanup(list.close)
	aliases, children := make(map[string]bool), make(map[string]bool)
	resolved, err := list.protectOrdinary(source, bound, boundFD, []rule{source.rule}, aliases, children)
	if err != nil || resolved || len(aliases) != 1 {
		t.Fatalf("an unresolved named origin was discarded: resolved=%v, aliases=%v, err=%v", resolved, aliases, err)
	}
}

func backingNamespace(t *testing.T, name string) bool {
	t.Helper()
	const marker = "JAILTEST_BACKING_NAMESPACE"
	if os.Getenv(marker) == name {
		if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
			t.Fatal(err)
		}
		return true
	}
	inMode(t, testMode{"namespaces", true})
	cmd := exec.CommandContext(t.Context(), testExe(t), "-test.run=^"+name+"$")
	cmd.Env = append(os.Environ(), marker+"="+name)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}, GidMappingsEnableSetgroups: false}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the private backing-topology fixture: %v\n%s", err, out)
	}
	return false
}

func backingMountCheck(t *testing.T, mounts []execMount, fds []int, name string, writable, noexec bool) {
	t.Helper()
	if len(mounts) != len(fds) {
		t.Fatal("executable mount descriptors are not aligned")
	}
	for i, mount := range mounts {
		var st unix.Stat_t
		if err := unix.Fstat(fds[i], &st); err != nil || st.Dev != mount.Dev || st.Ino != mount.Ino {
			t.Fatalf("mount descriptor lost its identity: %v, %+v", err, mount)
		}
		if mount.Path == name {
			if (mount.Access&Write != 0) != writable || mount.noexec != noexec {
				t.Fatalf("mount %s has wrong protection: %+v", name, mount)
			}
			return
		}
	}
	t.Fatalf("no protection was retained for %s: %+v", name, mounts)
}

func TestDirectoryExecProtectsHostBindMountAliases(t *testing.T) {
	if !backingNamespace(t, "TestDirectoryExecProtectsHostBindMountAliases") {
		return
	}
	root := hiddenPath(t.TempDir())
	parent, alias := root+"/parent", root+"/alias"
	for _, name := range []string{parent + "/child", alias} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount(parent+"/child", alias, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Unmount(alias, unix.MNT_DETACH) }()
	bound, parentFD := backingTestBind(t, parent)
	mirror, aliasFD := backingTestBind(t, alias)
	r := rule{Path: parent, Real: bound.Real, Access: Read | Exec, Dir: true, FD: parentFD, Dev: bound.Dev, Ino: bound.Ino}
	c := &setup{Binds: []bind{bound, mirror}, Rules: []rule{r}}
	before := slices.Clone(c.Rules)
	mounts, fds, err := execMounts(c, []int{parentFD, aliasFD})
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	if err != nil {
		t.Fatal(err)
	}
	backingMountCheck(t, mounts, fds, alias, false, true)
	backingMountCheck(t, mounts, fds, parent, false, false)
	if !slices.Equal(before, c.Rules) {
		t.Fatal("directory alias propagation created a Landlock grant")
	}
}

func TestDirectoryExecCarriesProtectionAcrossChildFilesystems(t *testing.T) {
	if !backingNamespace(t, "TestDirectoryExecCarriesProtectionAcrossChildFilesystems") {
		return
	}
	for _, writable := range []bool{false, true} {
		t.Run("child-write="+strconv.FormatBool(writable), func(t *testing.T) {
			root := hiddenPath(t.TempDir())
			parent, filesystem, alias := root+"/parent", root+"/parent/fs", root+"/alias"
			for _, name := range []string{filesystem, alias} {
				if err := os.MkdirAll(name, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := unix.Mount("tmpfs", filesystem, "tmpfs", 0, "mode=0755"); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Unmount(filesystem, unix.MNT_DETACH) }()
			if err := os.Mkdir(filesystem+"/nested", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mount(filesystem+"/nested", alias, "", unix.MS_BIND, ""); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Unmount(alias, unix.MNT_DETACH) }()
			bound, parentFD := backingTestBind(t, parent)
			childFS, filesystemFD := backingTestBind(t, filesystem)
			mirror, aliasFD := backingTestBind(t, alias)
			if childFS.Dev == bound.Dev {
				t.Fatal("the child-filesystem fixture did not change device")
			}
			c := &setup{Binds: []bind{bound, childFS, mirror}, Rules: []rule{
				{Path: parent, Real: bound.Real, Access: Read | Exec, Dir: true, FD: parentFD, Dev: bound.Dev, Ino: bound.Ino},
			}}
			if writable {
				fd, st, real := aliasTestSource(t, filesystem+"/nested")
				c.Rules = append(c.Rules, rule{Path: filesystem + "/nested", Real: real, Access: Read | Write | Exec,
					Dir: true, FD: fd, Dev: st.Dev, Ino: st.Ino})
			}
			before := slices.Clone(c.Rules)
			mounts, fds, err := execMounts(c, []int{parentFD, filesystemFD, aliasFD})
			defer func() {
				for _, fd := range fds {
					_ = unix.Close(fd)
				}
			}()
			if err != nil {
				t.Fatal(err)
			}
			backingMountCheck(t, mounts, fds, filesystem, false, false)
			backingMountCheck(t, mounts, fds, alias, writable, true)
			if !slices.Equal(before, c.Rules) {
				t.Fatal("child-filesystem propagation created a Landlock grant")
			}
		})
	}
}
