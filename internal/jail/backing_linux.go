package jail

import (
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type backingMount struct {
	major, minor uint64
	root, point  string
}

type backingRelations struct {
	aliases  []string
	children []string
}

func backingError(detail string) error {
	return failure("cannot establish the jail's mount topology: "+detail,
		"Keep the host's mount table and bound paths stable during start.")
}

func readBackingMounts() ([]backingMount, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, stepFailed("cannot read the host's mount topology",
			"Make /proc/self/mountinfo readable and keep bound paths stable during start.", err)
	}
	return parseBackingMounts(string(data))
}

// parseBackingMounts retains every record, including stacked mountpoints.
// It has no scanner token limit. Only a literal space separates fields.
func parseBackingMounts(text string) ([]backingMount, error) {
	var mounts []backingMount
	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			continue
		}
		fields := strings.FieldsFunc(line, func(c rune) bool { return c == ' ' })
		separator := slices.Index(fields, "-")
		if separator < 6 || len(fields) != separator+4 {
			return nil, backingError("a mountinfo record has incomplete fields")
		}
		for _, index := range []int{0, 1} {
			if _, err := strconv.ParseUint(fields[index], 10, 64); err != nil {
				return nil, backingError("a mountinfo record has an invalid mount ID")
			}
		}
		device := strings.Split(fields[2], ":")
		if len(device) != 2 {
			return nil, backingError("a mountinfo record has an invalid device")
		}
		major, majorErr := strconv.ParseUint(device[0], 10, 32)
		minor, minorErr := strconv.ParseUint(device[1], 10, 32)
		if majorErr != nil || minorErr != nil {
			return nil, backingError("a mountinfo record has an invalid device")
		}
		options := strings.Split(fields[5], ",")
		if slices.Contains(options, "rw") == slices.Contains(options, "ro") {
			return nil, backingError("a mountinfo record has inconsistent mount permissions")
		}
		root, err := backingPath(fields[3])
		if err != nil {
			return nil, err
		}
		point, err := backingPath(fields[4])
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, backingMount{major: major, minor: minor, root: root, point: point})
	}
	if len(mounts) == 0 {
		return nil, backingError("mountinfo contains no mount records")
	}
	return mounts, nil
}

func backingPath(field string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] == 0 {
			return "", backingError("a mountinfo path contains a zero byte")
		}
		if field[i] != '\\' {
			_ = out.WriteByte(field[i])
			continue
		}
		if i+3 >= len(field) {
			return "", backingError("a mountinfo path has an incomplete escape")
		}
		value := 0
		for j := 1; j <= 3; j++ {
			digit := field[i+j]
			if digit < '0' || digit > '7' {
				return "", backingError("a mountinfo path has an invalid octal escape")
			}
			value = value*8 + int(digit-'0')
		}
		if value == 0 || value > 255 {
			return "", backingError("a mountinfo path has an invalid escaped byte")
		}
		_ = out.WriteByte(byte(value))
		i += 3
	}
	decoded := out.String()
	if !path.IsAbs(decoded) || path.Clean(decoded) != decoded {
		return "", backingError("a mountinfo path is not absolute and clean")
	}
	return decoded, nil
}

func backingRelative(name, parent string) (string, bool) {
	if name == parent {
		return ".", true
	}
	if parent == "/" {
		return strings.TrimPrefix(name, "/"), true
	}
	if strings.HasPrefix(name, parent+"/") {
		return strings.TrimPrefix(name, parent+"/"), true
	}
	return "", false
}

// backingPaths includes all matching records. Choosing the longest textual
// mountpoint would discard possible roots of hidden or stacked mounts.
func backingPaths(real string, dev uint64, mounts []backingMount) ([]string, error) {
	if !path.IsAbs(real) || path.Clean(real) != real || strings.IndexByte(real, 0) >= 0 {
		return nil, backingError("a pinned path is not absolute and clean")
	}
	var paths []string
	seen := make(map[string]bool)
	for _, mount := range mounts {
		if mount.major != uint64(unix.Major(dev)) || mount.minor != uint64(unix.Minor(dev)) {
			continue
		}
		relative, below := backingRelative(real, mount.point)
		if !below {
			continue
		}
		candidate := path.Join(mount.root, relative)
		if !seen[candidate] {
			seen[candidate] = true
			paths = append(paths, candidate)
		}
	}
	if len(paths) == 0 {
		return nil, backingError("a pinned path has no matching filesystem record")
	}
	return paths, nil
}

func possibleBackingRelations(source *execSource, b bind, mounts []backingMount) (backingRelations, error) {
	var result backingRelations
	if source.Dev != b.Dev {
		return result, nil // represented child filesystems have their own queued source
	}
	if len(source.locations) == 0 {
		return result, backingError("a protected executable object has no verified path")
	}
	bound, err := backingPaths(b.Real, b.Dev, mounts)
	if err != nil {
		return result, err
	}
	aliases, children := make(map[string]bool), make(map[string]bool)
	for _, origin := range source.locations {
		roots, err := backingPaths(origin.real, source.Dev, mounts)
		if err != nil {
			return result, err
		}
		for _, root := range roots {
			for _, candidate := range bound {
				if relative, below := backingRelative(root, candidate); below {
					name := path.Join(b.Path, relative)
					if !aliases[name] {
						aliases[name] = true
						result.aliases = append(result.aliases, name)
					}
				}
				if relative, below := backingRelative(candidate, root); below && relative != "." && !children[relative] {
					children[relative] = true
					result.children = append(result.children, relative)
				}
			}
		}
	}
	return result, nil
}

// verifiedBackingChild uses mount text only to propose a relative name.
// The held directory, exact object identity and verified lineage decide
// whether that candidate is a protected capability.
func verifiedBackingChild(source *execSource, b bind, relative string, rules []rule) (*execSource, error) {
	if path.IsAbs(relative) || path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return nil, backingError("a physical alias has no safe relative path")
	}
	name, real := path.Join(source.Path, relative), path.Join(source.Real, relative)
	fd, err := openBeneath(source.fd, source.Path, name)
	if err != nil {
		return nil, pinError("cannot verify a possible executable child alias "+quote(name),
			"Use bound aliases whose physical relationship can be verified.", err)
	}
	fail := func(err error) (*execSource, error) {
		_ = unix.Close(fd)
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail(stepFailed("cannot inspect a possible executable child alias", "Keep bound paths unchanged during start.", err))
	}
	if st.Dev != b.Dev || st.Ino != b.Ino || (st.Mode&unix.S_IFMT == unix.S_IFDIR) != (b.Kind == kindDir) {
		return fail(backingError("a possible executable child alias names another object"))
	}
	line, err := lineage(fd, real)
	if err != nil {
		return fail(pinError("cannot verify the executable child's origin",
			"Keep bound paths unchanged during start.", err))
	}
	at := slices.Index(line, devIno{source.Dev, source.Ino})
	if at < 0 || len(source.origin) == 0 || source.origin[0] != (devIno{source.Dev, source.Ino}) {
		return fail(backingError("a possible executable child has no verified protected parent"))
	}
	// The new walk proves child-to-source containment. The retained origin
	// carries source-to-explicit-rule containment across child filesystems.
	origin := append(slices.Clone(line[:at]), source.origin...)
	access, err := executableAccess(origin, rules)
	if err != nil {
		return fail(err)
	}
	if access&Exec == 0 {
		return fail(backingError("a protected child lost its explicit executable origin"))
	}
	child := &execSource{rule: rule{Path: name, Real: real, Access: access, Dir: b.Kind == kindDir,
		Dev: b.Dev, Ino: b.Ino}, fd: fd, origin: origin}
	child.addLocation(real, origin)
	return child, nil
}

func (source *execSource) addLocation(real string, line []devIno) {
	for _, old := range source.locations {
		if old.real == real && slices.Equal(old.line, line) {
			return
		}
	}
	source.locations = append(source.locations, execLocation{real: real, line: slices.Clone(line)})
}

// rememberSource takes ownership of the source descriptor. Identities come
// only from original rules and named binds, so the worklist is finite.
func (list *execMountList) rememberSource(source *execSource) error {
	reject := func(err error) error {
		_ = unix.Close(source.fd)
		source.fd = -1
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(source.fd, &st); err != nil {
		return reject(stepFailed("cannot retain a protected executable object", "Keep bound paths unchanged during start.", err))
	}
	object := devIno{source.Dev, source.Ino}
	if st.Dev != source.Dev || st.Ino != source.Ino || (st.Mode&unix.S_IFMT == unix.S_IFDIR) != source.Dir ||
		len(source.origin) == 0 || source.origin[0] != object || source.Access&Exec == 0 {
		return reject(backingError("a protected executable source lost its identity or origin"))
	}
	if list.sources == nil {
		list.sources = make(map[devIno]*execSource)
	}
	if old := list.sources[object]; old != nil {
		if old.Dir != source.Dir || (old.Access&Write != 0) != (source.Access&Write != 0) {
			return reject(failure("the protected executable aliases "+quote(old.Path)+" and "+quote(source.Path)+" disagree on Write",
				"Give this executable object consistent Write permission through every alias."))
		}
		for _, location := range source.locations {
			old.addLocation(location.real, location.line)
		}
		_ = unix.Close(source.fd)
		source.fd = -1
		return nil
	}
	list.sources[object] = source
	list.work = append(list.work, source)
	return nil
}

func (list *execMountList) closeSources() {
	for _, source := range list.work {
		_ = unix.Close(source.fd)
		source.fd = -1
	}
	list.sources, list.work = nil, nil
}

func (list *execMountList) attachBindLocations(binds []bind, fds []int) error {
	for i, b := range binds {
		source := list.sources[devIno{b.Dev, b.Ino}]
		if source == nil {
			continue
		}
		line, err := lineage(fds[i], b.Real)
		if err != nil {
			return pinError("cannot verify a named executable alias", "Keep bound paths unchanged during start.", err)
		}
		if line[0] != (devIno{source.Dev, source.Ino}) {
			return backingError("a named executable alias changed its object")
		}
		source.addLocation(b.Real, line)
	}
	return nil
}
