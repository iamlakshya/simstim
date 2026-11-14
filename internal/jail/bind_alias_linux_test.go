package jail

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func namedBindFixture(t *testing.T, name string, write bool) (bind, *os.File, []devIno) {
	t.Helper()
	fd, st, real := aliasTestSource(t, name)
	owned := os.NewFile(uintptr(aliasTestCopy(t, fd)), name)
	t.Cleanup(func() { _ = owned.Close() })
	line, err := lineage(fd, real)
	if err != nil {
		t.Fatal(err)
	}
	return bind{Path: name, Real: real, Write: write, Kind: kindOf(st.Mode), Dev: st.Dev, Ino: st.Ino}, owned, line
}

func TestBindTopologyHasNoDependencyWithoutRelevantPairs(t *testing.T) {
	load := func() ([]backingMount, error) {
		t.Fatal("an empty or compatible setup read mountinfo")
		return nil, nil
	}
	for _, binds := range [][]bind{nil, {{Path: "/one", Kind: kindDir, Write: true}},
		{{Path: "/one", Kind: kindDir, Write: true}, {Path: "/other", Kind: kindDir, Write: true}},
		{{Path: "/one", Kind: kindFile}, {Path: "/other", Kind: kindFile, Write: true}}} {
		if err := checkBackingBindPairs(binds, nil, load); err != nil {
			t.Fatalf("an irrelevant bind pair was refused: %v", err)
		}
	}
}

func TestBindTopologyPositiveMismatchesDoNotProveOverlap(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{root + "/parent/wrong", root + "/other"} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parent, parentFile, _ := namedBindFixture(t, root+"/parent", true)
	child, childFile, _ := namedBindFixture(t, root+"/other", false)
	device := fmt.Sprintf("%d:%d", unix.Major(parent.Dev), unix.Minor(parent.Dev))
	text := "1 0 " + device + " / / rw - fixture none rw\n" +
		"2 1 " + device + " " + backingTestPath(parent.Real+"/wrong") + " " + backingTestPath(child.Real) + " rw - fixture none rw\n"
	mounts, err := parseBackingMounts(text)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkBackingBindPairs([]bind{parent, child}, []*os.File{parentFile, childFile},
		func() ([]backingMount, error) { return mounts, nil }); err != nil {
		t.Fatalf("text alone refused unrelated objects: %v", err)
	}
}

func TestBindTopologyTriesLaterCandidatesForPositiveProof(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{root + "/parent/wrong", root + "/parent/right", root + "/alias"} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parent, parentFile, _ := namedBindFixture(t, root+"/parent", true)
	child, childFile, _ := namedBindFixture(t, root+"/parent/right", false)
	// The child's recorded spelling models a mounted root outside parent.
	child.Real = root + "/alias"
	device := fmt.Sprintf("%d:%d", unix.Major(parent.Dev), unix.Minor(parent.Dev))
	text := "1 0 " + device + " / / rw - fixture none rw\n" +
		"2 1 " + device + " " + backingTestPath(parent.Real+"/wrong") + " " + backingTestPath(child.Real) + " rw - fixture none rw\n" +
		"3 1 " + device + " " + backingTestPath(parent.Real+"/right") + " " + backingTestPath(child.Real) + " rw - fixture none rw\n"
	mounts, err := parseBackingMounts(text)
	if err != nil {
		t.Fatal(err)
	}
	err = checkBackingBindPairs([]bind{parent, child}, []*os.File{parentFile, childFile},
		func() ([]backingMount, error) { return mounts, nil })
	if err == nil || !strings.Contains(err.Error(), "with other rights") || hintOf(err) == "" {
		t.Fatalf("the later verified overlap was missed: %v", err)
	}
}

func pinNamedPair(t *testing.T, parent, child string, parentWrite, childWrite bool) error {
	t.Helper()
	left, _, leftLine := namedBindFixture(t, parent, parentWrite)
	right, _, rightLine := namedBindFixture(t, child, childWrite)
	if err := checkAliases([]bind{left, right}, [][]devIno{leftLine, rightLine}); err != nil {
		t.Fatalf("the fixture must bypass ordinary lineage to exercise the new check: %v", err)
	}
	p, err := pin(checked{binds: []Bind{{Path: parent, Write: parentWrite}, {Path: child, Write: childWrite}}}, 0)
	p.close()
	return err
}

func TestNamedBindsRefuseDifferentRightsAcrossHostMountRoots(t *testing.T) {
	if !backingNamespace(t, "TestNamedBindsRefuseDifferentRightsAcrossHostMountRoots") {
		return
	}
	root := hiddenPath(t.TempDir())
	filesystem, alias := root+"/filesystem", root+"/alias"
	for _, name := range []string{filesystem, alias} {
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount("tmpfs", filesystem, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Unmount(filesystem, unix.MNT_DETACH) }()
	if err := os.Mkdir(filesystem+"/sub", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(filesystem+"/sub", alias, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Unmount(alias, unix.MNT_DETACH) }()
	err := pinNamedPair(t, filesystem, alias, true, false)
	if err == nil || !strings.Contains(err.Error(), "with other rights") || hintOf(err) == "" {
		t.Fatalf("different rights through mount roots were accepted: %v", err)
	}
	if err := pinNamedPair(t, filesystem, alias, true, true); err != nil {
		t.Fatalf("compatible mount aliases were refused: %v", err)
	}
}

func TestNamedBindsRefuseChildFilesystemAliasesInBothModes(t *testing.T) {
	if !backingNamespace(t, "TestNamedBindsRefuseChildFilesystemAliasesInBothModes") {
		return
	}
	root := hiddenPath(t.TempDir())
	parent, filesystem, alias := root+"/parent", root+"/parent/filesystem", root+"/alias"
	for _, name := range []string{filesystem, alias} {
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount("tmpfs", filesystem, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Unmount(filesystem, unix.MNT_DETACH) }()
	if err := os.Mkdir(filesystem+"/sub", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(filesystem+"/sub", alias, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Unmount(alias, unix.MNT_DETACH) }()
	// pin has no namespace-mode input. This refusal therefore holds before
	// either namespace or fallback confinement is selected for the command.
	err := pinNamedPair(t, parent, alias, true, false)
	if err == nil || !strings.Contains(err.Error(), "with other rights") || hintOf(err) == "" {
		t.Fatalf("a child-filesystem alias bypassed different-Write refusal: %v", err)
	}
}

func TestNamedBindHiddenParentImagesRemainUnproven(t *testing.T) {
	if !backingNamespace(t, "TestNamedBindHiddenParentImagesRemainUnproven") {
		return
	}
	for _, deep := range []bool{false, true} {
		t.Run(fmt.Sprintf("deeper=%v", deep), func(t *testing.T) {
			root := hiddenPath(t.TempDir())
			parent, cover, alias := root+"/parent", root+"/parent/sub", root+"/alias"
			for _, name := range []string{cover, alias} {
				if err := os.MkdirAll(name, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			underlying := cover
			if deep {
				underlying += "/deeper"
				if err := os.Mkdir(underlying, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := unix.Mount(underlying, alias, "", unix.MS_BIND, ""); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Unmount(alias, unix.MNT_DETACH) }()
			if err := unix.Mount("tmpfs", cover, "tmpfs", 0, "mode=0755"); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Unmount(cover, unix.MNT_DETACH) }()
			// The current lookup sees another inode, or ENOENT for deeper.
			// A later non-recursive parent bind would expose the hidden
			// original object, so this is an unknown image, not disjointness.
			err := pinNamedPair(t, parent, alias, true, false)
			if err == nil || !strings.Contains(err.Error(), "mount image") || hintOf(err) == "" {
				t.Fatalf("an unproven non-recursive image was accepted or unexplained: %v", err)
			}
		})
	}
}
