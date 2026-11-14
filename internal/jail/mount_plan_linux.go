package jail

import (
	"errors"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

type mountStep struct {
	name       string
	dir        bool
	executable bool
	object     devIno
	fd         int
	bound      bind
	exception  execMount
}

type mountGraph struct {
	steps        []mountStep
	parents      []map[int]bool
	conflict     error
	appliedNames []string
}

func newMountGraph(binds []bind, fds []int, execs []execMount, execFDs []int) *mountGraph {
	g := &mountGraph{}
	for i, b := range binds {
		g.steps = append(g.steps, mountStep{name: b.Path, dir: b.Kind == kindDir,
			object: devIno{b.Dev, b.Ino}, fd: fds[i], bound: b})
	}
	for i, r := range execs {
		g.steps = append(g.steps, mountStep{name: r.Path, dir: r.Dir, executable: true,
			object: devIno{r.Dev, r.Ino}, fd: execFDs[i], exception: r})
	}
	g.parents = make([]map[int]bool, len(g.steps))
	for i, a := range g.steps {
		for j, b := range g.steps {
			if i != j && a.dir && a.name != b.name && beneath(b.name, a.name) {
				g.before(i, j)
			}
			if a.name == b.name && a.rank() < b.rank() {
				g.before(i, j)
			}
		}
	}
	return g
}

// before adds one constraint. A rebuild is permitted only after this
// returns true, so each retry makes progress in a finite input graph.
func (g *mountGraph) before(parent, child int) bool {
	if parent == child || g.parents[child][parent] {
		return false
	}
	if g.parents[child] == nil {
		g.parents[child] = make(map[int]bool)
	}
	g.parents[child][parent] = true
	return true
}

func (s mountStep) rank() int {
	if !s.executable {
		return 0
	}
	if s.exception.noexec {
		return 1
	}
	return 2
}

func (g *mountGraph) stable(at int, rank []int) bool {
	if rank == nil {
		return true
	}
	for parent := range g.parents[at] {
		if rank[parent] < 0 {
			return false
		}
	}
	return true
}

func (g *mountGraph) targets(root int, dev uint64, rank []int) ([]mountTrace, error) {
	paths := make([]mountTrace, len(g.steps))
	lookupErrors := make([]error, len(g.steps))
	blocked := make([]bool, len(g.steps))
	for i, s := range g.steps {
		if !g.stable(i, rank) {
			blocked[i] = true
			continue // a pending provider can still change the links here
		}
		paths[i], lookupErrors[i] = traceMountTarget(root, dev, s.name, s.dir)
	}
	if rank != nil {
		// Do not commit facts derived from a provisional child trace.
		// Find pending directories whose contents supply a component or
		// link lookup. Entering a directory solely for .. is not a content
		// dependency and must not create an artificial cycle.
		providers := make([][]int, len(g.steps))
		for parent, step := range g.steps {
			if !step.dir || paths[parent].path == "" || lookupErrors[parent] != nil {
				continue
			}
			unchanged := g.providerImagePresent(root, dev, parent, paths, rank)
			for child, trace := range paths {
				if child == parent || trace.path == paths[parent].path {
					continue
				}
				for _, prefix := range trace.prefixes {
					if beneath(prefix, paths[parent].path) {
						if unchanged {
							paths[child].transparent = append(paths[child].transparent, parent)
						} else if rank[parent] < 0 {
							providers[child] = append(providers[child], parent)
							blocked[child] = true
						}
						break
					}
				}
			}
		}
		for child := range paths {
			for _, parent := range providers[child] {
				if !blocked[parent] {
					paths[child].providers = append(paths[child].providers, parent)
				}
			}
		}
	}
	for i := range paths {
		if blocked[i] {
			paths[i].path = ""
		} else if lookupErrors[i] != nil {
			if rank == nil {
				return nil, lookupErrors[i]
			}
			paths[i].path = ""
			paths[i].problem = lookupErrors[i]
		}
	}
	return paths, nil
}

// A directory already showing the pinned source, without completed child
// mounts beneath it, has the same non-recursive directory image that this
// bind would supply. Content lookups through compatible aliases need not
// wait for a redundant provider mount. Exact-target permission ordering is
// still enforced separately.
func (g *mountGraph) providerImagePresent(root int, dev uint64, at int, paths []mountTrace, rank []int) bool {
	name := paths[at].path
	for other, trace := range paths {
		if rank[other] >= 0 && (trace.path == "" || len(g.appliedNames) != 0 && g.appliedNames[other] != trace.path) {
			return false // a completed walk changed, so its provider order needs assessment
		}
		if other != at && rank[other] >= 0 && trace.path != "" && trace.path != name && beneath(trace.path, name) {
			return false
		}
	}
	target, err := resolveTarget(root, dev, name, true, false)
	if err != nil {
		return false
	}
	defer target.close()
	var st unix.Stat_t
	return unix.Fstat(target.dstFD, &st) == nil && (devIno{st.Dev, st.Ino}) == g.steps[at].object
}

// observe discovers ancestry in the current private root, including links
// supplied by binds. rank records actual completion order, or -1. A new
// constraint that an earlier mount violated requires a fresh private root.
func (g *mountGraph) observe(paths []mountTrace, rank []int) bool {
	g.conflict = g.conflictAt(paths)
	rebuild := false
	add := func(parent, child int) {
		if g.before(parent, child) && rank[child] >= 0 && (rank[parent] < 0 || rank[parent] > rank[child]) {
			rebuild = true
		}
	}
	for child, trace := range paths {
		for _, parent := range trace.providers {
			add(parent, child)
			if rebuild {
				return true
			}
		}
	}
	for i, a := range g.steps {
		for j, b := range g.steps {
			if i == j || paths[i].path == "" || paths[j].path == "" || !g.stable(i, rank) || !g.stable(j, rank) {
				continue
			}
			if paths[i].path == paths[j].path {
				if a.object != b.object || a.executable && b.executable && (a.exception.Access&Write != 0) != (b.exception.Access&Write != 0) {
					continue // provisional conflicts are rejected before final audit
				}
				if a.rank() < b.rank() {
					add(i, j)
					if rebuild {
						return true
					}
				}
				continue
			}
			if a.dir {
				if slices.Contains(paths[j].transparent, i) {
					continue
				}
				for _, prefix := range paths[j].prefixes {
					if beneath(prefix, paths[i].path) {
						add(i, j)
						if rebuild {
							return true
						}
						break
					}
				}
			}
		}
	}
	return rebuild
}

func (g *mountGraph) conflictAt(paths []mountTrace) error {
	for i, a := range g.steps {
		for j, b := range g.steps[:i] {
			if paths[i].path == "" || paths[i].path != paths[j].path {
				continue
			}
			if a.object != b.object {
				return failure("the jail's paths "+quote(a.name)+" and "+quote(b.name)+" resolve to one target with different objects",
					"Use bound paths whose in-root targets do not conflict.")
			}
			if a.executable && b.executable && (a.exception.Access&Write != 0) != (b.exception.Access&Write != 0) {
				return failure("the executable paths "+quote(a.name)+" and "+quote(b.name)+" resolve to one target with different Write permissions",
					"Give one effective executable target a consistent Write permission.")
			}
		}
	}
	return nil
}

func traceProblem(paths []mountTrace) error {
	for _, trace := range paths {
		if trace.problem != nil {
			return trace.problem
		}
	}
	return nil
}

func (g *mountGraph) ready(rank []int) []int {
	var out []int
	for i := range g.steps {
		if rank[i] >= 0 {
			continue
		}
		ready := true
		for parent := range g.parents[i] {
			if rank[parent] < 0 {
				ready = false
				break
			}
		}
		if ready {
			out = append(out, i)
		}
	}
	slices.SortStableFunc(out, func(a, b int) int {
		if mountNameBefore(g.steps[a], g.steps[b]) {
			return -1
		}
		if mountNameBefore(g.steps[b], g.steps[a]) {
			return 1
		}
		return 0
	})
	return out
}

func retryableMountTarget(err error) bool {
	var target *targetFailure
	return errors.As(err, &target) && (errors.Is(err, errMissingMountTarget) || errors.Is(err, errMountTargetKind) ||
		errors.Is(err, errExecutableTargetChanged) || errors.Is(err, unix.ENOTDIR))
}

func (g *mountGraph) apply(root int, dev uint64, at int, sub bool) error {
	s := g.steps[at]
	if s.executable {
		return execInto(root, dev, s.exception, s.fd)
	}
	return bindInto(root, dev, s.bound, s.fd, sub)
}

// mountPlanned never starts an untrusted program. Each replay adds an edge
// and replaces only this stage's private tmpfs. A cycle stops with a hint.
func mountPlanned(root int, dev uint64, g *mountGraph, sub bool, reset func(int) (int, uint64, error)) (int, uint64, error) {
	return g.run(root, dev, reset, func(root int, dev uint64, at int) error {
		return g.apply(root, dev, at, sub)
	})
}

// run keeps scheduling testable without allowing a counterfactual unit
// schedule to perform mount syscalls in the test runner's namespace.
func (g *mountGraph) run(root int, dev uint64, reset func(int) (int, uint64, error), apply func(int, uint64, int) error) (int, uint64, error) {
	for {
		rank := make([]int, len(g.steps))
		g.appliedNames = make([]string, len(g.steps))
		for i := range rank {
			rank[i] = -1
		}
		complete := 0
		rebuild := false
		for {
			paths, err := g.targets(root, dev, rank)
			if err != nil {
				return root, dev, err
			}
			rebuild = g.observe(paths, rank)
			if rebuild {
				break
			}
			if complete == len(g.steps) {
				return root, dev, g.verify(root, dev, paths)
			}
			ready := g.ready(rank)
			ready = slices.DeleteFunc(ready, func(at int) bool { return paths[at].path == "" || !g.stable(at, rank) })
			if len(ready) == 0 {
				if err := traceProblem(paths); err != nil {
					return root, dev, err
				}
				if g.conflict != nil {
					return root, dev, g.conflict
				}
				return root, dev, failure("the jail's mount paths have a dependency cycle",
					"Remove cyclic target aliases or use paths with a consistent parent order.")
			}
			// A missing prefix can be supplied by another ready bind. Do
			// not weaken the target walk to create inside a host directory.
			var deferred error
			advanced := false
			for _, next := range ready {
				if err := apply(root, dev, next); err != nil {
					if !retryableMountTarget(err) {
						return root, dev, err
					}
					if deferred == nil {
						deferred = err
					}
					continue
				}
				rank[next] = complete
				g.appliedNames[next] = paths[next].path
				complete++
				advanced = true
				break
			}
			if !advanced {
				if g.conflict != nil {
					return root, dev, g.conflict
				}
				return root, dev, deferred
			}
		}
		var err error
		root, dev, err = reset(root)
		if err != nil {
			return root, dev, err
		}
	}
}

func (g *mountGraph) verify(root int, dev uint64, paths []mountTrace) error {
	if err := traceProblem(paths); err != nil {
		return err
	}
	if err := g.conflictAt(paths); err != nil {
		return err
	}
	for i, s := range g.steps {
		if reservedProcTrace(paths[i]) {
			return failure("the mount target for "+quote(s.name)+" enters the jail's reserved /proc",
				"Use bound paths outside /proc. The jail supplies its own process filesystem.")
		}
		target, err := resolveTarget(root, dev, s.name, s.dir, false)
		if err != nil {
			return err
		}
		var st unix.Stat_t
		var fs unix.Statfs_t
		err = errors.Join(unix.Fstat(target.dstFD, &st), unix.Fstatfs(target.dstFD, &fs))
		target.close()
		if err != nil {
			return stepFailed("cannot verify a final jail mount", "Keep bound paths unchanged during start.", err)
		}
		if (devIno{st.Dev, st.Ino}) != s.object || target.path != paths[i].path {
			return failure("the final jail mount at "+quote(s.name)+" changed or was hidden",
				"Use paths whose effective mount targets do not conflict.")
		}
		readonly := !s.bound.Write
		noexec := s.bound.Write
		nodev := s.bound.Kind != kindDevice
		if s.executable {
			readonly = s.exception.Access&Write == 0
			noexec = s.exception.noexec
			nodev = true
		}
		for j, other := range g.steps {
			if paths[i].path == paths[j].path && other.executable && !other.exception.noexec {
				noexec = false // an explicit rule covers this same effective mount
			}
		}
		flags := fs.Flags
		if readonly && flags&unix.ST_RDONLY == 0 || noexec && flags&unix.ST_NOEXEC == 0 ||
			nodev && flags&unix.ST_NODEV == 0 || flags&unix.ST_NOSUID == 0 {
			return failure("the final jail mount at "+quote(s.name)+" lost its protection",
				"Use paths whose effective mount permissions do not conflict.")
		}
	}
	return nil
}

func reservedProcTrace(trace mountTrace) bool {
	if beneath(trace.path, "/proc") {
		return true
	}
	for _, name := range trace.prefixes {
		if beneath(name, "/proc") {
			return true
		}
	}
	return false
}

// resetMountRoot discards only the new root in this private namespace.
func resetMountRoot(root int) (int, uint64, error) {
	_ = unix.Close(root)
	if err := unmount("/newroot"); err != nil {
		return -1, 0, err
	}
	if err := mount("tmpfs", "/newroot", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
		return -1, 0, err
	}
	fd, err := openPath(unix.AT_FDCWD, "/newroot", unix.RESOLVE_NO_SYMLINKS|unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		return -1, 0, pinError("cannot rebuild the jail's private root", "Keep mount paths stable during start.", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, 0, stepFailed("cannot inspect the rebuilt jail root", "Report this as a bug.", err)
	}
	return fd, st.Dev, nil
}

// Keep source-order ties deterministic without treating lexical order as
// the ancestry relation. Tests can inspect the graph independently of mounts.
func mountNameBefore(a, b mountStep) bool {
	if c := strings.Compare(a.name, b.name); c != 0 {
		return c < 0
	}
	return a.rank() < b.rank()
}
