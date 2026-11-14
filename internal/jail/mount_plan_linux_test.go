package jail

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func graphForTest(binds []bind, execs []execMount) *mountGraph {
	bindFDs, execFDs := make([]int, len(binds)), make([]int, len(execs))
	// These fixtures supply graph metadata, never actual mount sources.
	for i := range bindFDs {
		bindFDs[i] = -1
	}
	for i := range execFDs {
		execFDs[i] = -1
	}
	return newMountGraph(binds, bindFDs, execs, execFDs)
}

func graphDir(path string, inode uint64) bind {
	return bind{Path: path, Kind: kindDir, Dev: 1, Ino: inode}
}

func graphFile(path string, inode uint64) bind {
	return bind{Path: path, Kind: kindFile, Dev: 1, Ino: inode}
}

func TestMountGraphWaitsForItsDeclaredProviders(t *testing.T) {
	g := graphForTest([]bind{graphDir("/a", 1), graphDir("/a/p", 2), graphFile("/a/p/leaf", 3)}, nil)
	rank := []int{0, -1, -1}
	if g.stable(2, rank) {
		t.Error("a pending directory provider did not block its child trace")
	}
	if got := g.ready(rank); !slices.Equal(got, []int{1}) {
		t.Errorf("ready with the provider pending = %v, want only provider1", got)
	}
	rank[1] = 1
	if !g.stable(2, rank) {
		t.Error("the child stayed blocked after its providers completed")
	}
	if got := g.ready(rank); !slices.Equal(got, []int{2}) {
		t.Errorf("ready after the provider = %v, want only child2", got)
	}
}

func TestMountGraphDefersAProvisionalTargetConflict(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.MkdirAll(root+"/a/p", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/b/q", root+"/a/p/leaf"); err != nil {
		t.Fatal(err)
	}
	fd, dev := targetRoot(t, root)
	g := graphForTest([]bind{graphDir("/a", 1), graphDir("/a/p", 2), graphFile("/a/p/leaf", 3), graphFile("/b/q", 4)}, nil)
	rank := []int{0, -1, -1, -1}
	paths, err := g.targets(fd, dev, rank)
	if err != nil {
		t.Fatal(err)
	}
	if paths[2].path != "" {
		t.Errorf("the child trace was trusted before /a/p mounted: %+v", paths[2])
	}
	if g.observe(paths, rank) || g.conflictAt(paths) != nil {
		t.Fatal("an unresolved child caused a rebuild or final target conflict")
	}
	// Replace the visible directory's link as a later non-recursive parent
	// bind would do. The old provisional collision must leave no constraint.
	replaceGraphLink(t, root+"/a/p/leaf", "/c/r")
	rank[1] = 1
	paths, err = g.targets(fd, dev, rank)
	if err != nil {
		t.Fatal(err)
	}
	if paths[2].path != "/c/r" || paths[3].path != "/b/q" {
		t.Fatalf("final targets = %+v, want distinct /c/r and /b/q", paths)
	}
	if g.observe(paths, rank) || g.conflictAt(paths) != nil {
		t.Fatal("distinct final targets kept a provisional conflict")
	}
	if got := g.ready(rank); !slices.Contains(got, 2) || !slices.Contains(got, 3) {
		t.Errorf("distinct final targets are blocked: %v", got)
	}
}

func TestMountGraphDoesNotTurnProvisionalAncestryIntoACycle(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.MkdirAll(root+"/a/p", 0o700); err != nil {
		t.Fatal(err)
	}
	for name, link := range map[string]string{"left": "/z", "right": "/z/sub"} {
		if err := os.Symlink(link, root+"/a/p/"+name); err != nil {
			t.Fatal(err)
		}
	}
	fd, dev := targetRoot(t, root)
	g := graphForTest([]bind{graphDir("/a", 1), graphDir("/a/p", 2), graphDir("/a/p/left", 3), graphDir("/a/p/right", 4)}, nil)
	rank := []int{0, -1, -1, -1}
	paths, err := g.targets(fd, dev, rank)
	if err != nil || g.observe(paths, rank) {
		t.Fatalf("provisional parent stage = %v, want no replay", err)
	}
	replaceGraphLink(t, root+"/a/p/left", "/y/sub")
	replaceGraphLink(t, root+"/a/p/right", "/y")
	rank[1] = 1
	paths, err = g.targets(fd, dev, rank)
	if err != nil || g.observe(paths, rank) {
		t.Fatalf("final parent stage = %v, want no replay of unmounted children", err)
	}
	if got := g.ready(rank); !slices.Equal(got, []int{3}) {
		t.Fatalf("final parent /y is not ready before /y/sub: %v", got)
	}
	rank[3] = 2
	if got := g.ready(rank); !slices.Equal(got, []int{2}) {
		t.Errorf("the final child stayed in a false cycle: %v", got)
	}
}

func TestMountGraphReplaysOnlyForANewCanonicalDependency(t *testing.T) {
	g := graphForTest([]bind{graphFile("/early", 1), graphDir("/later", 2)}, nil)
	paths := []mountTrace{
		{path: "/resolved/tool", prefixes: []string{"/", "/resolved"}},
		{path: "/resolved", prefixes: []string{"/"}},
	}
	// The canonical parent was discovered after both steps completed in the
	// wrong order. One new constraint earns one private-root replay.
	rank := []int{0, 1}
	if !g.observe(paths, rank) {
		t.Fatal("a newly discovered late canonical parent did not request replay")
	}
	if g.observe(paths, rank) {
		t.Fatal("the same existing constraint earned another replay")
	}
	rank = []int{-1, -1}
	if got := g.ready(rank); !slices.Equal(got, []int{1}) {
		t.Fatalf("replay forgot the discovered parent order: %v", got)
	}
	rank[1] = 0
	if got := g.ready(rank); !slices.Equal(got, []int{0}) {
		t.Errorf("the child did not become ready after its canonical parent: %v", got)
	}
}

func TestMountGraphUsesAVisitedProviderOutsideTheFinalTarget(t *testing.T) {
	g := graphForTest([]bind{graphFile("/a/sub/link/file", 1), graphDir("/z/root", 2)}, nil)
	paths := []mountTrace{
		{path: "/real/file", prefixes: []string{"/", "/a", "/a/sub", "/real"}},
		{path: "/a/sub", prefixes: []string{"/", "/z", "/a"}},
	}
	rank := []int{-1, -1}
	if g.observe(paths, rank) {
		t.Fatal("an unmounted child requested a private-root replay")
	}
	if got := g.ready(rank); !slices.Equal(got, []int{1}) {
		t.Errorf("a visited link provider outside the final target was ignored: %v", got)
	}
}

func TestMountGraphOrdersSameTargetShadowsBeforeExplicitExec(t *testing.T) {
	b := graphDir("/write", 9)
	b.Write = true
	execs := []execMount{
		{rule: rule{Path: "/alias", Dir: true, Dev: 1, Ino: 9, Access: Read | Exec}, noexec: true},
		{rule: rule{Path: "/write", Dir: true, Dev: 1, Ino: 9, Access: Read | Exec}},
	}
	g := graphForTest([]bind{b}, execs)
	paths := []mountTrace{
		{path: "/write", prefixes: []string{"/"}},
		{path: "/write", prefixes: []string{"/"}},
		{path: "/write", prefixes: []string{"/"}},
	}
	rank := []int{-1, -1, -1}
	for _, next := range []int{0, 1, 2} {
		if g.observe(paths, rank) || g.conflictAt(paths) != nil {
			t.Fatal("compatible same-object protection mounts caused a replay or conflict")
		}
		if got := g.ready(rank); !slices.Equal(got, []int{next}) {
			t.Fatalf("ready = %v, want step%d before the later protection rank", got, next)
		}
		rank[next] = next
	}
	execs[1].Access |= Write
	if err := graphForTest([]bind{b}, execs).conflictAt(paths); err == nil || !strings.Contains(err.Error(), "different Write permissions") || hintOf(err) == "" {
		t.Errorf("contradictory same-target permissions = %v, want a conflict with a hint", err)
	}
}

func replaceGraphLink(t *testing.T, path, link string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link, path); err != nil {
		t.Fatal(err)
	}
}

func TestMountGraphDiscoversALaterAliasedProviderBeforeFinalConflict(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{"a/sub", "c"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a/sub/link", "c/link"} {
		if err := os.Symlink("/b/file", filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	fd, dev := targetRoot(t, root)
	g := graphForTest([]bind{graphDir("/a", 1), graphFile("/a/sub/link", 2), graphDir("/c", 3),
		graphFile("/c/link", 4), graphDir("/z", 5), graphDir("/z/root", 6)}, nil)
	rank := []int{0, 1, 2, -1, -1, -1}
	paths, err := g.targets(fd, dev, rank)
	if err != nil {
		t.Fatal(err)
	}
	if g.conflictAt(paths) == nil {
		t.Fatal("the fixture did not expose its provisional collision")
	}
	if g.observe(paths, rank) {
		t.Fatal("a provisional collision required a replay before its provider was discovered")
	}
	if got := g.ready(rank); !slices.Contains(got, 4) {
		t.Fatalf("the still-pending /z provider cannot make progress: %v", got)
	}
	if err := os.Mkdir(root+"/z", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/a/sub", root+"/z/root"); err != nil {
		t.Fatal(err)
	}
	rank[4] = 3
	paths, err = g.targets(fd, dev, rank)
	if err != nil || !g.observe(paths, rank) {
		t.Fatalf("the newly visible late provider did not require a replay: %v", err)
	}
	// Replaying /z/root installs the real submount contents before the
	// earlier child is retried. Its final object no longer collides.
	replaceGraphLink(t, root+"/a/sub/link", "/d/file")
	rank = []int{0, -1, 1, -1, 2, 3}
	paths, err = g.targets(fd, dev, rank)
	if err != nil || g.observe(paths, rank) || g.conflictAt(paths) != nil {
		t.Fatalf("the completed provider left a false final conflict: %v", err)
	}
	if paths[1].path != "/d/file" || paths[3].path != "/b/file" {
		t.Errorf("replayed final targets = %+v, want distinct /d/file and /b/file", paths)
	}
}

func TestMountGraphAllowsPopOnlyVisitsWithoutAFalseCycle(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Mkdir(root+"/links", 0o700); err != nil {
		t.Fatal(err)
	}
	for name, link := range map[string]string{"a": "/b/../a", "b": "/a/../b"} {
		if err := os.Symlink(link, root+"/links/"+name); err != nil {
			t.Fatal(err)
		}
	}
	fd, dev := targetRoot(t, root)
	g := graphForTest([]bind{graphDir("/links", 1), graphDir("/links/a", 2), graphDir("/links/b", 3)}, nil)
	rank := []int{0, -1, -1}
	paths, err := g.targets(fd, dev, rank)
	if err != nil || g.observe(paths, rank) {
		t.Fatalf("pop-only target walks = %v, want no replay", err)
	}
	if paths[1].path != "/a" || paths[2].path != "/b" {
		t.Fatalf("pop-only directory visits made valid targets provisional: %+v", paths)
	}
	if got := g.ready(rank); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("independent final targets were put in a false cycle: %v", got)
	}
}

func TestMountGraphAllowsAlreadySatisfiedCompatibleAliasProviders(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{"work/a", "work/b"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, link := range map[string]string{
		"work/a/return": "/work/b", "work/b/return": "/work/a",
		"work/aliasA": "/work/b/return", "work/aliasB": "/work/a/return",
	} {
		if err := os.Symlink(link, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	fd, dev := targetRoot(t, root)
	var binds []bind
	var sources []int
	for _, c := range []struct{ path, source string }{
		{"/work", root + "/work"}, {"/work/aliasA", root + "/work/a"}, {"/work/aliasB", root + "/work/b"},
	} {
		source, err := openPath(unix.AT_FDCWD, c.source, unix.RESOLVE_NO_SYMLINKS|unix.RESOLVE_NO_MAGICLINKS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = unix.Close(source) })
		var st unix.Stat_t
		if err := unix.Fstat(source, &st); err != nil {
			t.Fatal(err)
		}
		binds = append(binds, bind{Path: c.path, Real: c.source, Kind: kindDir, Dev: st.Dev, Ino: st.Ino})
		sources = append(sources, source)
	}
	g := newMountGraph(binds, sources, nil, nil)
	g.appliedNames = []string{"/work", "", ""}
	rank := []int{0, -1, -1}
	paths, err := g.targets(fd, dev, rank)
	if err != nil || g.observe(paths, rank) {
		t.Fatalf("already satisfied directory providers = %v, want no replay", err)
	}
	if paths[1].path != "/work/a" || paths[2].path != "/work/b" {
		t.Fatalf("compatible exact-object aliases were made cyclic: %+v", paths)
	}
	if got := g.ready(rank); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("compatible aliases cannot progress in either order: %v", got)
	}
	g.steps[1].object.ino ^= 1
	if g.providerImagePresent(fd, dev, 1, paths, rank) {
		t.Error("a different provider inode was treated as the visible directory image")
	}
	g.steps[1].object.ino ^= 1
	g.steps[1].object.dev ^= 1
	if g.providerImagePresent(fd, dev, 1, paths, rank) {
		t.Error("a different provider device was treated as the visible directory image")
	}
	g.steps[1].object.dev ^= 1
	g.appliedNames[0] = "/old-work"
	if g.providerImagePresent(fd, dev, 1, paths, rank) {
		t.Error("a changed completed walk was treated as the same directory image")
	}
	g.appliedNames[0] = "/work"
	paths[0].path = ""
	if g.providerImagePresent(fd, dev, 1, paths, rank) {
		t.Error("an unknown completed walk was treated as the same directory image")
	}
	paths[0].path = "/work"
	g.steps = append(g.steps, mountStep{name: "/work/a/tool", fd: -1})
	g.parents = append(g.parents, nil)
	g.appliedNames = append(g.appliedNames, "/work/a/tool")
	rank = append(rank, 1)
	paths = append(paths, mountTrace{path: "/work/a/tool"})
	if g.providerImagePresent(fd, dev, 1, paths, rank) {
		t.Error("a provider with a completed child mount was treated as unchanged")
	}
}

func TestMountGraphReportsATrueCycleWithoutMounting(t *testing.T) {
	root := hiddenPath(t.TempDir())
	fd, dev := targetRoot(t, root)
	g := graphForTest([]bind{graphDir("/a", 1), graphDir("/b", 2)}, nil)
	g.before(0, 1)
	g.before(1, 0)
	resets := 0
	_, _, err := g.run(fd, dev, func(old int) (int, uint64, error) {
		resets++
		t.Error("an established cycle attempted to rebuild the root")
		return old, dev, failure("a cycle reached the root rebuild callback", "Reject the cycle before doing mount work.")
	}, func(_ int, _ uint64, at int) error {
		t.Errorf("an established cycle attempted to apply mount step%d", at)
		return failure("a cycle reached the mount callback", "Reject the cycle before doing mount work.")
	})
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") || hintOf(err) == "" {
		t.Errorf("cyclic graph = %v, want a dependency-cycle error with a hint", err)
	}
	if resets != 0 {
		t.Errorf("an established cycle rebuilt its private root %d times", resets)
	}
}

func TestMountTraceKeepsLinkEntriesAndVirtualParentWalks(t *testing.T) {
	root := hiddenPath(t.TempDir())
	for _, name := range []string{"provider", "final"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/final/", root+"/provider/link"); err != nil {
		t.Fatal(err)
	}
	fd, dev := targetRoot(t, root)
	trace, err := traceMountTarget(fd, dev, "/missing/../provider/link/./sub/..", true)
	if err != nil || trace.path != "/final" {
		t.Fatalf("trace = %+v, %v, want /final", trace, err)
	}
	for _, prefix := range []string{"/", "/provider", "/final"} {
		if !slices.Contains(trace.prefixes, prefix) {
			t.Errorf("trace lost the visited provider prefix %s: %v", prefix, trace.prefixes)
		}
	}
	for _, name := range []string{"/missing", "/provider/link", "/final/sub"} {
		if slices.Contains(trace.prefixes, name) {
			t.Errorf("an entry or pop-only visit became a directory-content dependency: %s", name)
		}
	}
	for _, entry := range []string{"/missing", "/provider", "/provider/link", "/final", "/final/sub"} {
		if !slices.Contains(trace.entries, entry) {
			t.Errorf("trace lost an entered path needed for the final audit: %s", entry)
		}
	}
	for _, name := range []string{"missing", "final/sub"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("planning created the virtual path %s: %v", name, err)
		}
	}
}

func TestFinalMountAuditRejectsReservedProcessWalks(t *testing.T) {
	g := graphForTest([]bind{graphFile("/alias", 1)}, nil)
	for _, path := range []string{"/proc", "/proc/self", "/proc/self/fd/3"} {
		// The destination can be outside /proc after a later parent walk.
		// Looking up content in the reserved provider must be rejected.
		trace := mountTrace{path: "/elsewhere", prefixes: []string{"/", path}, entries: []string{"/alias", path, "/elsewhere"}}
		err := g.verify(-1, 0, []mountTrace{trace})
		if err == nil || !strings.Contains(err.Error(), "reserved /proc") || hintOf(err) == "" {
			t.Errorf("reserved trace through %s = %v, want a refusal with a hint", path, err)
		}
	}
	if !reservedProcTrace(mountTrace{path: "/proc/self", prefixes: []string{"/"}}) {
		t.Error("a final target inside the reserved process filesystem was allowed")
	}
}

func TestFinalMountAuditAllowsAPopOnlyProcessDirectoryVisit(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.Mkdir(root+"/proc", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/safe", []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/../safe", root+"/alias"); err != nil {
		t.Fatal(err)
	}
	fd, dev := targetRoot(t, root)
	trace, err := traceMountTarget(fd, dev, "/alias", false)
	if err != nil || trace.path != "/safe" || !slices.Contains(trace.entries, "/proc") {
		t.Fatalf("pop-only process directory trace = %+v, %v, want /safe via /proc/..", trace, err)
	}
	if reservedProcTrace(trace) {
		t.Errorf("a pop-only visit was mistaken for reading process data: %+v", trace)
	}
}

func TestFinalMountAuditRejectsWrongObjectsAndMissingReadOnly(t *testing.T) {
	root := hiddenPath(t.TempDir())
	if err := os.WriteFile(root+"/file", []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, dev := targetRoot(t, root)
	var st unix.Stat_t
	if err := unix.Stat(root+"/file", &st); err != nil {
		t.Fatal(err)
	}
	b := bind{Path: "/file", Kind: kindFile, Dev: st.Dev, Ino: st.Ino}
	trace := mountTrace{path: "/file", prefixes: []string{"/"}, entries: []string{"/file"}}
	g := graphForTest([]bind{b}, nil)
	g.steps[0].object.ino ^= 1
	if err := g.verify(fd, dev, []mountTrace{trace}); err == nil || !strings.Contains(err.Error(), "changed or was hidden") || hintOf(err) == "" {
		t.Errorf("wrong final object = %v, want an identity error with a hint", err)
	}
	g.steps[0].object.ino = st.Ino
	trace.path = "/other"
	if err := g.verify(fd, dev, []mountTrace{trace}); err == nil || !strings.Contains(err.Error(), "changed or was hidden") || hintOf(err) == "" {
		t.Errorf("wrong final target = %v, want a target error with a hint", err)
	}
	trace.path = "/file"
	// Creating the fixture proved this mount is writable. It must never
	// pass as a completed read-only bind without an actual protected mount.
	if err := g.verify(fd, dev, []mountTrace{trace}); err == nil || !strings.Contains(err.Error(), "lost its protection") || hintOf(err) == "" {
		t.Errorf("unprotected writable final mount = %v, want a protection error with a hint", err)
	}
}
