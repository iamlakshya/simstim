//go:build linux

package host

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

type eventDirectory struct {
	root      *os.Root
	file      *os.File
	reference cachefs.Reference
	ancestor  bool
	identity  os.FileInfo
	onlyName  string
	watch     int
}

// EventsDiscovery owns read-only native notifications on the original private
// session/socket roots. A notification only asks the bridge to reconcile its
// actual supervisor connections; it is never a session or lifecycle fact.
// The existing bridge owner calls Next. This type starts no reader goroutine.
type EventsDiscovery struct {
	file            *os.File
	native          int
	gate            sync.Mutex
	reading, closed bool
	readers         sync.WaitGroup
	directories     map[string]eventDirectory
	closeOnce       sync.Once
	closeErr        error
}

// EventsDiscoverySupported reports whether the helper has native discovery.
func EventsDiscoverySupported() bool { return true }

// OpenEventsDiscovery anchors the existing roots without creating or changing
// any directory. An inotify queue overflow or root replacement ends the watch.
func OpenEventsDiscovery() (*EventsDiscovery, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("open session discovery: %w", err)
	}
	d := &EventsDiscovery{file: os.NewFile(uintptr(fd), "session-discovery"), native: fd, directories: map[string]eventDirectory{}}
	if d.file == nil {
		_ = unix.Close(fd)
		return nil, eventsDiscoveryError("the session discovery descriptor is unavailable")
	}
	for _, path := range []string{paths.StateDir()} {
		if err := d.addDirectory(path); err != nil {
			return nil, errors.Join(err, d.Close())
		}
	}
	if err := d.refresh(); err != nil {
		return nil, errors.Join(err, d.Close())
	}
	return d, nil
}

func eventsDiscoveryError(message string) error {
	return UserError("events_unavailable", message, "Run events again after checking the host's private state and runtime folders.")
}

func (d *EventsDiscovery) addDirectory(path string) error {
	if pin, exists := d.directories[path]; exists {
		return checkEventDirectory(path, pin)
	}
	root, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		return err
	}
	reference, err := cachefs.CaptureRoot(root)
	if err != nil {
		_ = root.Close()
		return err
	}
	file, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return err
	}
	// The kernel follows this owned descriptor, never a later replacement at
	// path. IN_ONLYDIR checks the descriptor's object in the same system call.
	watch, err := unix.InotifyAddWatch(d.native, fmt.Sprintf("/proc/self/fd/%d", file.Fd()), unix.IN_ONLYDIR|unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF)
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return fmt.Errorf("watch the original session directory: %w", err)
	}
	if err := reference.CheckRoot(root); err != nil {
		_ = file.Close()
		_ = root.Close()
		return err
	}
	d.directories[path] = eventDirectory{root: root, file: file, reference: reference, watch: watch}
	return nil
}

func (d *EventsDiscovery) refresh() error {
	if err := d.addDirectory(paths.RuntimeDir()); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := d.addAncestor(filepath.Dir(paths.RuntimeDir()), filepath.Base(paths.RuntimeDir())); err != nil {
			return err
		}
	}
	for _, path := range []string{paths.SessionsDir(), filepath.Join(paths.RuntimeDir(), "s")} {
		if err := d.addDirectory(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for path, pin := range d.directories {
		if err := checkEventDirectory(path, pin); err != nil {
			return err
		}
	}
	return nil
}

// Next blocks on the original native descriptor until a directory changes.
// Cancellation closes that descriptor. The caller closes the owner afterwards
// to join its reader and release every anchored directory descriptor.
func (d *EventsDiscovery) Next(ctx context.Context) error {
	d.gate.Lock()
	if d.closed || d.reading {
		d.gate.Unlock()
		return eventsDiscoveryError("the session discovery reader is closed or already in use")
	}
	if err := ctx.Err(); err != nil {
		d.gate.Unlock()
		return err
	}
	d.reading = true
	d.readers.Add(1)
	err := d.refresh()
	d.gate.Unlock()
	defer func() { d.gate.Lock(); d.reading = false; d.gate.Unlock(); d.readers.Done() }()
	if err != nil {
		return err
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(joined)
		d.gate.Lock()
		d.closed = true
		d.gate.Unlock()
		_ = d.file.Close()
	})
	defer func() {
		if !stop() {
			<-joined
		}
	}()
	buffer := make([]byte, 64<<10)
	for {
		n, err := d.file.Read(buffer)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("read session discovery: %w", err)
		}
		relevant := false
		for offset := 0; offset < n; {
			if n-offset < unix.SizeofInotifyEvent {
				return eventsDiscoveryError("the native session notification is incomplete")
			}
			mask := binary.NativeEndian.Uint32(buffer[offset+4:])
			watch := int(int32(binary.NativeEndian.Uint32(buffer[offset:])))
			size := int(binary.NativeEndian.Uint32(buffer[offset+12:]))
			if size > n-offset-unix.SizeofInotifyEvent {
				return eventsDiscoveryError("the native session notification exceeds its read")
			}
			if mask&(unix.IN_Q_OVERFLOW|unix.IN_MOVE_SELF|unix.IN_DELETE_SELF|unix.IN_IGNORED) != 0 {
				return eventsDiscoveryError("session discovery lost events or an original directory changed")
			}
			if d.relevantChange(watch, buffer[offset+unix.SizeofInotifyEvent:offset+unix.SizeofInotifyEvent+size]) {
				relevant = true
			}
			offset += unix.SizeofInotifyEvent + size
		}
		if !relevant {
			continue
		}
		d.gate.Lock()
		defer d.gate.Unlock()
		if d.closed {
			if err := ctx.Err(); err != nil {
				return err
			}
			return os.ErrClosed
		}
		err = d.refresh()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}

// Close closes and joins the actual native reader before releasing its roots.
func (d *EventsDiscovery) Close() error {
	d.closeOnce.Do(func() {
		d.gate.Lock()
		d.closed = true
		d.gate.Unlock()
		if err := d.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			d.closeErr = err
		}
		d.readers.Wait()
		for _, pin := range d.directories {
			d.closeErr = errors.Join(d.closeErr, pin.file.Close(), pin.root.Close())
		}
	})
	return d.closeErr
}
