// Package jail confines a trusted-side process that handles untrusted
// input: the VMM, the image converter, host-side git.
//
// A jail is not a wall. A wall holds untrusted code, the agent. A jail holds
// trusted code that reads untrusted bytes, so that a bug in it reaches as
// little as possible. Where the host gives a user namespace, a jail gets its
// own mount, PID, IPC, UTS and cgroup namespaces, an empty network namespace
// with NetNone, and a subuid where one is set up. Every jail also sets
// no_new_privs, drops every capability, and loads Landlock rules and a seccomp
// profile. Without a user namespace, Landlock and seccomp stand in for the
// namespaces (isolation spec 5.6).
//
// The jail's set-up runs in the helper itself, started again: a program that
// calls Command calls Init first in main.
package jail

import (
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Spec says how to confine one command.
type Spec struct {
	Argv        []string
	Env         []string     // the command's whole environment, and nothing else
	Subuid      bool         // run as a subuid when /etc/subuid gives a range and newuidmap exists
	Net         NetMode      // NetNone: an empty network namespace. NetHost: the host's.
	Binds       []Bind       // the only paths in the new mount namespace
	Rules       []PathRule   // Landlock rules, at the best ABI the kernel has
	Seccomp     Profile      // "vmm", "converter", "git", "egress"
	Files       []*os.File   // inherited from fd 3
	FileRulesV1 []FileRuleV1 // exact rights on selected regular inherited files
}

// NetMode says which network a jail has.
type NetMode int

const (
	// NetNone is an empty network namespace. Without a user namespace,
	// seccomp refuses AF_INET and AF_INET6 sockets instead.
	NetNone NetMode = iota
	// NetHost is the host's network, for the egress process.
	NetHost
)

// Profile names a jail's seccomp profile.
type Profile string

// The seccomp profiles. Each refuses the system calls that no jail needs,
// such as new namespaces, mounts, ptrace, bpf and io_uring. The egress
// profile also allows network sockets. The others allow Unix sockets only.
const (
	ProfileVMM       Profile = "vmm"
	ProfileConverter Profile = "converter"
	ProfileGit       Profile = "git"
	ProfileEgress    Profile = "egress"
)

// Bind is one host path that a jail shows, at the same path inside. The
// jail pins the object that the path names when Command runs: a path that
// names another object at the start makes the jail fail. A path may pass
// through ordinary symbolic links, but never through /proc's magic links.
// With a user namespace, a bound folder does not show the mounts below it.
type Bind struct {
	Path  string // absolute and clean
	Write bool   // read and write, or else read-only
}

// Access is a set of Landlock rights beneath a path.
type Access uint8

// The rights of a PathRule. A rule adds rights. It never takes them away.
const (
	Read      Access = 1 << iota // read files and list folders
	Write                        // write, truncate, make and remove files and folders
	Exec                         // run files with execve
	Ioctl                        // use ioctl on device files (Landlock ABI 5)
	Connect                      // connect to Unix sockets by path (Landlock ABI 9)
	allAccess = Read | Write | Exec | Ioctl | Connect
)

// PathRule gives a jail more Landlock rights beneath a path that a Bind
// shows, or on /proc/self. The path is opened beneath its bind with no
// symbolic link on the way. A Bind already gives read and run rights, or
// read and write rights, ioctl on a device, and connect on a socket.
type PathRule struct {
	Path   string
	Access Access
}

// procSelf is the one path outside the binds that a PathRule may name.
const procSelf = "/proc/self"

// minABI is the Landlock ABI that a jail needs without a user namespace:
// older ABIs cannot refuse truncate (Linux 6.2).
const minABI = 3

// failure is an error of a jail, with what to try next.
func failure(message, hint string) error {
	return &proto.Error{Code: "jail", Message: message, Hint: hint}
}

// checked is a Spec that passed every check of its text.
type checked struct {
	argv        []string
	binds       []Bind // parents first
	rules       []PathRule
	execFD      int // the inherited file that Argv[0] names as /proc/self/fd/<n>, or 0
	fileRulesV1 []FileRuleV1
}

// check refuses a Spec that no jail can run, before anything starts. It
// checks supplied file handles, but paths are pinned on the host later.
func check(s Spec) (checked, error) {
	var c checked
	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return c, failure("a jail needs a command to run", "Give Spec.Argv the program and its arguments.")
	}
	if !knownProfile(s.Seccomp) {
		return c, failure("unknown seccomp profile "+quote(string(s.Seccomp)),
			"Name one of vmm, converter, git or egress in Spec.Seccomp.")
	}
	if s.Net != NetNone && s.Net != NetHost {
		return c, failure("unknown network mode", "Use NetNone or NetHost in Spec.Net.")
	}
	for _, file := range s.Files {
		if file == nil {
			return c, failure("a jail's inherited file is missing", "Give Spec.Files only open files.")
		}
		if !fileOpen(file) {
			return c, failure("a jail's inherited file is closed", "Give Spec.Files only open files.")
		}
	}
	var err error
	c.fileRulesV1, err = checkFileRulesV1(s.FileRulesV1, len(s.Files))
	if err != nil {
		return c, err
	}
	binds, err := checkBinds(s.Binds)
	if err != nil {
		return c, err
	}
	if err := checkRules(s.Rules, binds); err != nil {
		return c, err
	}
	prog := s.Argv[0]
	if n, ok := inheritedFD(prog, len(s.Files)); ok {
		c.execFD = n
	} else if !path.IsAbs(prog) {
		return c, failure("the jail's command "+quote(prog)+" is not an absolute path",
			"Name the program by its full path in Spec.Argv, or as /proc/self/fd/<n> for one of Spec.Files.")
	} else if !beneathAny(path.Clean(prog), binds) {
		return c, failure("the jail's command "+quote(prog)+" is outside every bound path",
			"Add the program, or its folder, to Spec.Binds, or pass it in Spec.Files.")
	}
	c.argv = slices.Clone(s.Argv)
	c.binds = binds
	c.rules = mergeRules(s.Rules)
	return c, nil
}

// inheritedFD says whether prog is /proc/self/fd/<n> for one of the n
// inherited files, which start at fd 3.
func inheritedFD(prog string, files int) (int, bool) {
	rest, ok := strings.CutPrefix(prog, "/proc/self/fd/")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || strconv.Itoa(n) != rest || n < 3 || n >= 3+files {
		return 0, false
	}
	return n, true
}

// checkBinds checks the text of each bound path, and refuses two binds, one
// inside the other, with different rights. Without a user namespace, the
// outer bind's Landlock rule would give its rights inside the inner one too.
func checkBinds(in []Bind) ([]Bind, error) {
	out := slices.Clone(in)
	// Parents first, so that a path inside another bound folder lands on it.
	slices.SortFunc(out, func(a, b Bind) int { return strings.Compare(a.Path, b.Path) })
	for i, b := range out {
		p := b.Path
		if !path.IsAbs(p) || path.Clean(p) != p {
			return nil, failure("the bound path "+quote(p)+" is not absolute and clean",
				"Give Spec.Binds paths such as /usr/lib, with no .. and no trailing slash.")
		}
		if p == "/" || beneath(p, "/proc") {
			return nil, failure("a jail cannot bind "+quote(p),
				"Bind only the folders and files that the command needs. A jail has its own /proc.")
		}
		for _, o := range out[:i] {
			switch {
			case o.Path == p:
				return nil, failure("the path "+quote(p)+" is bound twice", "Bind each path once.")
			case beneath(p, o.Path) && o.Write != b.Write:
				return nil, failure("the bound path "+quote(p)+" is inside "+quote(o.Path)+", with other rights",
					"Bind the two paths with the same Write, or bind paths that do not overlap.")
			}
		}
	}
	return out, nil
}

// checkRules checks that each rule names rights beneath a path that the
// jail shows, or on /proc/self.
func checkRules(rules []PathRule, binds []Bind) error {
	for _, r := range rules {
		if !path.IsAbs(r.Path) || path.Clean(r.Path) != r.Path {
			return failure("the Landlock rule's path "+quote(r.Path)+" is not absolute and clean",
				"Give Spec.Rules paths such as /usr/lib, with no .. and no trailing slash.")
		}
		if r.Access == 0 || r.Access&^allAccess != 0 {
			return failure("the Landlock rule for "+quote(r.Path)+" names no known right",
				"Use Read, Write, Exec, Ioctl or Connect in PathRule.Access.")
		}
		if r.Path != procSelf && !slices.ContainsFunc(binds, func(b Bind) bool { return beneath(r.Path, b.Path) }) {
			return failure("the Landlock rule's path "+quote(r.Path)+" is outside every bound path",
				"Bind the path in Spec.Binds, or leave the rule out. Of /proc, a rule may name only /proc/self.")
		}
	}
	return nil
}

// mergeRules joins the rules on one path, with their rights combined, and
// sorts them by path, parents first. So the order that the caller gives
// changes nothing: a jail mounts each Exec rule's path in this order.
func mergeRules(in []PathRule) []PathRule {
	by := map[string]Access{}
	for _, r := range in {
		by[r.Path] |= r.Access
	}
	out := make([]PathRule, 0, len(by))
	for p, a := range by {
		out = append(out, PathRule{Path: p, Access: a})
	}
	slices.SortFunc(out, func(a, b PathRule) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func knownProfile(p Profile) bool {
	switch p {
	case ProfileVMM, ProfileConverter, ProfileGit, ProfileEgress:
		return true
	default:
		return false
	}
}

// beneath says whether p is root or a path inside it.
func beneath(p, root string) bool {
	return p == root || root == "/" || strings.HasPrefix(p, root+"/")
}

func beneathAny(p string, binds []Bind) bool {
	return slices.ContainsFunc(binds, func(b Bind) bool { return beneath(p, b.Path) })
}

func quote(s string) string { return `"` + s + `"` }

// mode is how a jail is built on this host.
type mode struct {
	userns bool   // namespaces, or else Landlock and seccomp alone
	sub    *subID // the subuid and subgid to run as, or nil for the login user
}

// choose picks how to build a jail from what the host offers: whether a
// user namespace works, the Landlock ABI, and the subordinate IDs that
// newuidmap can map. Without a user namespace, a jail needs Landlock ABI 3,
// and the git jail cannot run: git changes file modes by path, such as
// chmod of .git/config.lock, which such a jail refuses.
func choose(s Spec, userns bool, abi int, sub func() (subID, bool)) (mode, error) {
	if !userns {
		if s.Seccomp == ProfileGit {
			return mode{}, failure("the git jail needs a user namespace: git changes file modes by path, "+
				"which a jail without one refuses",
				"Run `"+brand.Name+" host prepare <host>` to give the host user namespaces.")
		}
		if abi < minABI {
			return mode{}, failure(
				"this host has no user namespace for the jail, and its Landlock ABI is "+strconv.Itoa(abi)+
					": a jail without a user namespace needs Landlock ABI 3 (Linux 6.2) or later",
				"Run `"+brand.Name+" host prepare <host>` to give the host user namespaces, or update its kernel to Linux 6.2 or later.")
		}
		return mode{}, nil
	}
	m := mode{userns: true}
	if s.Subuid {
		if id, ok := sub(); ok {
			m.sub = &id
		}
	}
	return m, nil
}
