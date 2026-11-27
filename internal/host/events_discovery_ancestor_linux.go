//go:build linux

package host

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// addAncestor observes only creation of a missing runtime root. It opens a
// checked safe ancestor read-only; unrelated public-directory names are ignored.
// Its notification grants no private-root or session authority.
func (d *EventsDiscovery) addAncestor(path, leaf string) error {
	if _, exists := d.directories[path]; exists {
		return nil
	}
	root, err := cachefs.OpenSafeAncestor(path)
	if err != nil {
		return err
	}
	file, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return err
	}
	identity, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return err
	}
	watch, err := unix.InotifyAddWatch(d.native, fmt.Sprintf("/proc/self/fd/%d", file.Fd()), unix.IN_ONLYDIR|unix.IN_CREATE|unix.IN_MOVED_TO|unix.IN_ATTRIB|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF)
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return err
	}
	d.directories[path] = eventDirectory{root: root, file: file, ancestor: true, identity: identity, onlyName: leaf, watch: watch}
	return nil
}

func checkEventDirectory(path string, pin eventDirectory) error {
	if !pin.ancestor {
		root, err := cachefs.OpenPrivateRoot(path, false)
		if err != nil {
			return err
		}
		defer root.Close()
		return pin.reference.CheckRoot(root)
	}
	root, err := cachefs.OpenSafeAncestor(path)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	identity, statErr := file.Stat()
	err = errors.Join(statErr, file.Close())
	if err != nil {
		return err
	}
	if !os.SameFile(identity, pin.identity) || identity.Mode() != pin.identity.Mode() {
		return eventsDiscoveryError("the original discovery ancestor changed")
	}
	return nil
}

func (d *EventsDiscovery) relevantChange(watch int, name []byte) bool {
	for _, pin := range d.directories {
		if pin.watch == watch {
			return pin.onlyName == "" || strings.TrimRight(string(name), "\x00") == pin.onlyName
		}
	}
	return false
}
