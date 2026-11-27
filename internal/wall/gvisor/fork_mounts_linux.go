//go:build linux

package gvisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/jail"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

// ForkMountsV1 is issued only by this original prepared factory while it
// captures the SAME mount vector used by the actual gofer. It has no data or
// path constructor. The host still proves its original protected boot owner.
type ForkMountsV1 struct {
	mu       sync.Mutex
	sandbox  *sandbox
	home     *freshForkHomeV1
	session  cachefs.Reference
	plan     []wall.Mount
	work     *os.File
	state    map[string]*os.File
	files    []*os.File
	routes   map[string]string
	closed   bool
	closing  bool
	closeErr error
}

func captureForkMountsV1(s *sandbox, plan []wall.Mount, original []*os.File) (result *ForkMountsV1, returned error) {
	if s == nil || s.storage == nil || s.storage.home == nil || len(plan) != len(original) {
		return nil, failure("wall_setup", "gvisor has no original fresh-home and mount-vector creator", nil)
	}
	if err := s.storage.home.check(); err != nil {
		return nil, err
	}
	owner := &ForkMountsV1{sandbox: s, home: s.storage.home, plan: slices.Clone(plan), state: make(map[string]*os.File), routes: make(map[string]string)}
	complete := false
	defer func() {
		if !complete {
			cleanup := owner.close()
			returned = errors.Join(returned, cleanup)
			if cleanup != nil {
				result = owner
			}
		}
	}()
	var err error
	owner.session, err = cachefs.CaptureRoot(s.storage.session)
	if err != nil {
		return nil, err
	}
	for index, mount := range plan {
		if original[index] == nil {
			return nil, failure("wall_setup", "the original gvisor mount vector has a missing source", nil)
		}
		identity, err := files.Identity(original[index])
		if err != nil || identity == "" || identity != mount.Identity {
			return nil, errors.Join(failure("wall_setup", "the original gvisor mount vector changed native identity", nil), err)
		}
		var name string
		if mount.Guest == "/work" {
			name = "/work"
		} else {
			for _, state := range s.spec.StateDirs {
				if mount.Guest == "/home/agent/"+state {
					name = state
					break
				}
			}
		}
		if name == "" {
			continue
		}
		if !mount.Write || original[index] == nil {
			return nil, failure("wall_setup", "an original gvisor fork mount lost its writable role", nil)
		}
		fd, err := unix.FcntlInt(original[index].Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), "original-gvisor-fork-mount")
		owner.files = append(owner.files, file)
		info, err := file.Stat()
		if err != nil || !info.IsDir() {
			return nil, errors.Join(fmt.Errorf("an original gvisor fork mount is not a directory"), err)
		}
		if name == "/work" {
			if owner.work != nil {
				return nil, fmt.Errorf("the original gvisor workspace mount repeats")
			}
			owner.work = file
		} else {
			if owner.state[name] != nil {
				return nil, fmt.Errorf("the original gvisor state mount repeats")
			}
			owner.state[name] = file
		}
	}
	if !s.smoke && owner.work == nil || len(owner.state) != len(s.spec.StateDirs) {
		return nil, failure("wall_setup", "the original gvisor fork mount vector omits workspace or state", nil)
	}
	// Bind each original inode only once in A. Exact same-source OCI aliases
	// route to that fixed A mount; generic duplicate-bind guards stay strict.
	roleFiles := make(map[string]*os.File, len(owner.state)+1)
	if owner.work != nil {
		roleFiles["/work"] = owner.work
	}
	for name, file := range owner.state {
		roleFiles["/home/agent/"+name] = file
	}
	for index, mount := range plan {
		actual, err := original[index].Stat()
		if err != nil {
			return nil, err
		}
		for fixed, file := range roleFiles {
			expected, err := file.Stat()
			if err != nil {
				return nil, err
			}
			if os.SameFile(actual, expected) {
				if !mount.Write {
					return nil, failure("wall_setup", "the original gvisor mount repeats a writable role with different rights", nil)
				}
				owner.routes[mount.Guest] = fixed
				break
			}
		}
	}
	complete = true
	return owner, nil
}

// CommandBindsV1 supplies fixed A mount aliases on the SAME actual source
// descriptors. It accepts no source path and verifies the protected session
// root and original captured gofer vector before exposing those aliases.
func (owner *ForkMountsV1) CommandBindsV1(ctx context.Context, protected *os.Root, spec wall.Spec, plan []wall.Mount, original []*os.File) ([]jail.Bind, map[string]string, error) {
	if owner == nil || ctx == nil || protected == nil {
		return nil, nil, failure("wall_setup", "the gvisor fork mount source has no original host operation", nil)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.closing || owner.sandbox == nil || owner.home == nil || owner.sandbox.storage == nil || owner.sandbox.spec.ID != spec.ID || owner.sandbox.storage.home != owner.home || !slices.Equal(owner.plan, plan) || len(plan) != len(original) {
		return nil, nil, failure("wall_setup", "the original gvisor fork mount source changed", nil)
	}
	if err := errors.Join(ctx.Err(), owner.session.CheckRoot(protected), owner.home.check()); err != nil {
		return nil, nil, err
	}
	for index, mount := range plan {
		identity, err := files.Identity(original[index])
		if err != nil || identity == "" || identity != mount.Identity {
			return nil, nil, errors.Join(failure("wall_setup", "the gvisor command substituted an original mount descriptor", nil), err)
		}
	}
	binds := []jail.Bind{{Path: "/home/agent", File: owner.home.file, Write: true}}
	if owner.work != nil {
		binds = append(binds, jail.Bind{Path: "/work", File: owner.work, Write: true})
	}
	names := slices.Clone(spec.StateDirs)
	slices.Sort(names)
	for _, name := range names {
		if name == "" || strings.HasPrefix(name, "/") || owner.state[name] == nil {
			return nil, nil, failure("wall_setup", "the gvisor command changed a fixed state mount role", nil)
		}
		binds = append(binds, jail.Bind{Path: "/home/agent/" + name, File: owner.state[name], Write: true})
	}
	routes := make(map[string]string, len(owner.routes))
	for guest, fixed := range owner.routes {
		routes[guest] = fixed
	}
	return binds, routes, nil
}

func (owner *ForkMountsV1) routeOCI(oci *ociSpec) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.closing || owner.home == nil || oci == nil {
		return failure("wall_setup", "the actual gvisor OCI has no original mount producer", nil)
	}
	if err := owner.home.check(); err != nil {
		return err
	}
	for index := range oci.Mounts {
		mount := &oci.Mounts[index]
		if fixed := owner.routes[mount.Destination]; fixed != "" {
			if mount.Type != "bind" {
				return failure("wall_setup", "an actual gvisor fork role lost its OCI bind class", nil)
			}
			mount.Source = fixed
		}
	}
	return nil
}

func (owner *ForkMountsV1) close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.closeErr != nil {
		return owner.closeErr
	}
	owner.closing = true
	for _, file := range owner.files {
		owner.closeErr = errors.Join(owner.closeErr, file.Close())
	}
	owner.closed = owner.closeErr == nil
	return owner.closeErr
}
