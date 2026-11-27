package jail

import (
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// execMount is a namespace mount, not another Landlock grant. An alternate
// writable exposure gets the same write protection while staying noexec,
// unless an explicit rule covers that path in the jail.
type execMount struct {
	rule
	noexec bool
}

type execLocation struct {
	real string
	line []devIno
}

// An origin carries verified ancestry back to an explicit rule. A derived
// source is still only a named bind object, never a new Landlock grant.
type execSource struct {
	rule
	fd        int
	origin    []devIno
	locations []execLocation
}

// execMountList owns exactly one descriptor per retained mount. add takes
// ownership even when a duplicate or an error means it cannot keep fd.
type execMountList struct {
	mounts      []execMount
	fds         []int
	sources     map[devIno]*execSource
	work        []*execSource
	backing     []backingMount
	backingRead bool
}

func (list *execMountList) close() {
	for _, fd := range list.fds {
		_ = unix.Close(fd)
	}
	list.mounts, list.fds = nil, nil
	list.closeSources()
}

func (list *execMountList) add(r execMount, fd int) error {
	reject := func(err error) error {
		_ = unix.Close(fd)
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return reject(stepFailed("cannot inspect the executable mount", "Keep bound paths unchanged during start.", err))
	}
	if st.Dev != r.Dev || st.Ino != r.Ino || (st.Mode&unix.S_IFMT == unix.S_IFDIR) != r.Dir {
		return reject(failure("the executable mount at "+quote(r.Path)+" changed",
			"Keep bound paths unchanged during start."))
	}
	for i, old := range list.mounts {
		if old.Path != r.Path {
			continue
		}
		if old.Dev != r.Dev || old.Ino != r.Ino || old.Dir != r.Dir {
			return reject(failure("the executable paths at "+quote(r.Path)+" name different objects",
				"Give each executable path one stable object."))
		}
		if (old.Access&Write != 0) != (r.Access&Write != 0) {
			return reject(failure("the executable paths at "+quote(r.Path)+" disagree on Write",
				"Give aliases of one executable object the same Write permission."))
		}
		list.mounts[i].Access |= r.Access
		// A false value is supplied only for an independently verified
		// explicit exception or matching physical and lexical coverage.
		list.mounts[i].noexec = old.noexec && r.noexec
		_ = unix.Close(fd)
		return nil
	}
	list.mounts = append(list.mounts, r)
	list.fds = append(list.fds, fd)
	return nil
}

// execMounts preserves each explicit exception and protects its represented
// aliases. It never adds an Exec right to commandRules.
func execMounts(c *setup, fds []int) ([]execMount, []int, error) {
	var owned execMountList
	defer owned.closeSources()
	paths := make([]string, len(c.Binds))
	for i, b := range c.Binds {
		paths[i] = b.Path
	}
	for _, r := range pinnedRules(c.Rules) {
		at := holder(paths, r.Path)
		if r.Access&Exec == 0 || at < 0 || !c.Binds[at].Write {
			continue
		}
		if err := owned.seedRule(c, fds, r); err != nil {
			owned.close()
			return nil, nil, err
		}
	}
	if err := owned.attachBindLocations(c.Binds, fds); err != nil {
		owned.close()
		return nil, nil, err
	}
	// Each physical source is drawn from the explicit rules or named binds
	// and enters work once. Verified child filesystems extend this closure.
	for at := 0; at < len(owned.work); at++ {
		for i, b := range c.Binds {
			if b.Write {
				if err := owned.protectBind(owned.work[at], b, fds[i], c.Rules); err != nil {
					owned.close()
					return nil, nil, err
				}
			}
		}
	}
	return owned.mounts, owned.fds, nil
}

func (list *execMountList) seedRule(c *setup, fds []int, r rule) error {
	fd, err := reopenRule(c, fds, r)
	if err != nil {
		return err
	}
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	line, err := lineage(fd, r.Real)
	if err != nil {
		return pinError("cannot locate the executable path "+quote(r.Path),
			"Keep bound paths unchanged during start.", err)
	}
	access, err := executableAccess(line, c.Rules)
	if err != nil {
		return err
	}
	// The declared exception is a capability in its own right. Keep it
	// even when an alias has more than one physical ancestor match.
	original, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return stepFailed("cannot retain the explicit executable path", "Report this as a bug.", err)
	}
	explicit := execMount{rule: r}
	explicit.Access = access
	if err := list.add(explicit, original); err != nil {
		return err
	}
	source := &execSource{rule: explicit.rule, fd: fd, origin: line}
	source.addLocation(r.Real, line)
	fd = -1 // rememberSource takes ownership, including on failure
	return list.rememberSource(source)
}

func (list *execMountList) protectBind(source *execSource, b bind, fd int, rules []rule) error {
	aliases, children := make(map[string]bool), make(map[string]bool)
	resolved, err := list.protectOrdinary(source, b, fd, rules, aliases, children)
	if err != nil {
		return err
	}
	if resolved {
		return nil
	}
	// Unlike filesystem roots cannot be compared as path strings. A
	// represented child is verified and queued in its own device above.
	if source.Dev != b.Dev {
		return nil
	}
	if !list.backingRead {
		mounts, err := readBackingMounts()
		if err != nil {
			return err
		}
		list.backing, list.backingRead = mounts, true
	}
	relations, err := possibleBackingRelations(source, b, list.backing)
	if err != nil {
		return err
	}
	for _, name := range relations.aliases {
		if err := list.protectAlias(source, b, fd, name, rules, aliases); err != nil {
			return err
		}
	}
	for _, relative := range relations.children {
		if !source.Dir {
			return backingError("a possible child lies beneath an executable file")
		}
		if err := list.protectChild(source, b, fd, relative, rules, children); err != nil {
			return err
		}
	}
	return nil
}

func (list *execMountList) protectOrdinary(source *execSource, b bind, fd int, rules []rule, aliases, children map[string]bool) (bool, error) {
	allAliases := len(source.locations) > 0
	for _, origin := range source.locations {
		names, err := exposureNames(b, origin.real, origin.line)
		if err != nil {
			return false, err
		}
		if len(names) == 0 {
			allAliases = false
		}
		for _, name := range names {
			if err := list.protectAlias(source, b, fd, name, rules, aliases); err != nil {
				return false, err
			}
		}
	}
	if !source.Dir {
		return allAliases, nil
	}
	line, err := lineage(fd, b.Real)
	if err != nil {
		return false, pinError("cannot locate the bound executable child", "Keep bound paths unchanged during start.", err)
	}
	parent := bind{Path: source.Path, Dev: source.Dev, Ino: source.Ino}
	names, err := exposureNames(parent, b.Real, line)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		relative, _ := backingRelative(name, source.Path)
		if relative != "." {
			if err := list.protectChild(source, b, fd, relative, rules, children); err != nil {
				return false, err
			}
		}
	}
	return allAliases || len(children) > 0, nil
}

func (list *execMountList) protectAlias(source *execSource, b bind, fd int, name string, rules []rule, seen map[string]bool) error {
	if seen[name] {
		return nil
	}
	if err := list.addAlias(b, fd, source.rule, name, source.Access, rules); err != nil {
		return pinError("cannot verify every physical alias of "+quote(source.Path),
			"Use bound aliases whose physical relationship can be verified.", err)
	}
	seen[name] = true
	return nil
}

func (list *execMountList) protectChild(source *execSource, b bind, fd int, relative string, rules []rule, seen map[string]bool) error {
	if seen[relative] {
		return nil
	}
	child, err := verifiedBackingChild(source, b, relative, rules)
	if err != nil {
		return err
	}
	line, err := lineage(fd, b.Real)
	if err != nil {
		_ = unix.Close(child.fd)
		return pinError("cannot locate the executable child's bound alias", "Keep bound paths unchanged during start.", err)
	}
	child.addLocation(b.Real, line)
	if err := list.rememberSource(child); err != nil {
		return err
	}
	seen[relative] = true
	return nil
}

func (list *execMountList) addAlias(b bind, fd int, r rule, name string, access Access, rules []rule) error {
	mirror, err := openBeneath(fd, b.Path, name)
	if err != nil {
		return pinError("cannot protect the executable alias "+quote(name),
			"Keep bound paths unchanged during start.", err)
	}
	real, line, err := executableLine(mirror, name)
	if err != nil {
		_ = unix.Close(mirror)
		return err
	}
	if line[0] != (devIno{r.Dev, r.Ino}) {
		_ = unix.Close(mirror)
		return failure("the executable alias "+quote(name)+" changed", "Keep bound paths unchanged during start.")
	}
	alias := execMount{rule: rule{Path: name, Access: access, Dir: r.Dir,
		Real: real, Dev: r.Dev, Ino: r.Ino}, noexec: !explicitExec(name, line, rules)}
	return list.add(alias, mirror)
}

// exposureNames keeps every matching ancestor position. A self bind can
// put one directory identity at several depths. Each candidate is reopened
// and identity-checked before it can become a mount.
func exposureNames(b bind, real string, line []devIno) ([]string, error) {
	parts := strings.Split(strings.TrimPrefix(real, "/"), "/")
	var names []string
	for at, object := range line {
		if object != (devIno{b.Dev, b.Ino}) {
			continue
		}
		if at > len(parts) {
			return nil, failure("the executable's ancestor chain has no matching path components",
				"Use executable paths whose physical aliases have a clear relative path.")
		}
		name := path.Join(append([]string{b.Path}, parts[len(parts)-at:]...)...)
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func executableLine(fd int, name string) (string, []devIno, error) {
	real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return "", nil, pinError("cannot locate the executable alias "+quote(name),
			"Keep bound paths unchanged during start.", err)
	}
	if strings.HasSuffix(real, " (deleted)") {
		return "", nil, failure("the executable alias "+quote(name)+" was deleted",
			"Keep bound paths unchanged during start.")
	}
	line, err := lineage(fd, real)
	if err != nil {
		return "", nil, pinError("cannot locate the executable alias's parents",
			"Keep bound paths unchanged during start.", err)
	}
	return real, line, nil
}

func explicitExec(name string, line []devIno, rules []rule) bool {
	for _, r := range pinnedRules(rules) {
		if r.Access&Exec == 0 || name != r.Path && !(r.Dir && beneath(name, r.Path)) {
			continue
		}
		at := slices.Index(line, devIno{r.Dev, r.Ino})
		if at == 0 || at > 0 && r.Dir {
			return true
		}
	}
	return false
}

// The nearest physical executable rule wins, as a child exception does
// beneath a parent exception. Aliases of one exact object may not silently
// disagree on Write, because neither spelling can confine that inode alone.
func executableAccess(line []devIno, rules []rule) (Access, error) {
	nearest := len(line)
	var access Access
	var chosen string
	for _, r := range pinnedRules(rules) {
		if r.Access&Exec == 0 {
			continue
		}
		at := slices.Index(line, devIno{r.Dev, r.Ino})
		if at < 0 || at > 0 && !r.Dir || at > nearest {
			continue
		}
		if at == nearest && (r.Access&Write != 0) != (access&Write != 0) {
			return 0, failure("the executable aliases "+quote(chosen)+" and "+quote(r.Path)+" disagree on Write",
				"Give aliases of one executable object the same Write permission.")
		}
		if at < nearest {
			nearest, access, chosen = at, r.Access, r.Path
		} else {
			access |= r.Access
		}
	}
	if access&Exec != 0 && access&Write == 0 && len(line) > 0 {
		if err := writableRuleConflict(line[0], chosen, rules); err != nil {
			return 0, err
		}
	}
	return access, nil
}

func writableRuleConflict(object devIno, chosen string, rules []rule) error {
	for _, r := range pinnedRules(rules) {
		if r.Access&Write != 0 && r.Access&Exec == 0 && (devIno{r.Dev, r.Ino}) == object {
			return failure("the read-only executable protection of "+quote(chosen)+" conflicts with the writable rule "+quote(r.Path),
				"Give this executable object consistent Write permission through every alias.")
		}
	}
	return nil
}
