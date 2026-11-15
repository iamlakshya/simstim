package provider

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/files"
)

// SourceConfigPin holds only current native configuration metadata witnesses.
// It supplies no sharing completeness, resource identity or source permission.
// The actual provider inspector owns those checks and the original locator.
type SourceConfigPin struct {
	mu     sync.Mutex
	source client.MachineConfigSource
	root   *os.Root
	file   *os.File
	closed bool
}

// CaptureSourceConfig observes the actual selected directory or its existing
// parent/absence witness. It creates no configuration directory.
func CaptureSourceConfig(path string) (client.MachineConfigSource, error) {
	source, pin, err := CaptureSourceConfigPinned(path)
	if err != nil {
		return client.MachineConfigSource{}, err
	}
	return source, pin.Close()
}

// CaptureSourceConfigPinned keeps the actually observed native object alive
// across capture and the closed creation operation. No original ID is allowed
// to become reusable before its retained pin has been acquired.
func CaptureSourceConfigPinned(path string) (_ client.MachineConfigSource, _ *SourceConfigPin, err error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return client.MachineConfigSource{}, nil, err
	}
	resolved, resolveErr := filepath.EvalSymlinks(absolute)
	var source client.MachineConfigSource
	var witnessed string
	if resolveErr == nil {
		witnessed = resolved
		source = client.MachineConfigSource{Role: client.MachineConfigExisting, Path: resolved}
	} else {
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return client.MachineConfigSource{}, nil, resolveErr
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return client.MachineConfigSource{}, nil, err
		}
		witnessed = parent
		source = client.MachineConfigSource{Role: client.MachineConfigAbsent, Path: filepath.Join(parent, filepath.Base(absolute))}
	}
	file, err := pinSourceConfigDirectory(witnessed)
	if err != nil {
		return client.MachineConfigSource{}, nil, err
	}
	var retained *SourceConfigPin
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil && retained != nil {
			err = errors.Join(err, retained.Close())
		}
	}()
	identity, err := files.Identity(file)
	if err != nil {
		return client.MachineConfigSource{}, nil, err
	}
	if source.Role == client.MachineConfigExisting {
		source.ObjectID = identity
	} else {
		source.ParentID = identity
	}
	pin, err := PinSourceConfig(source)
	if err != nil {
		return client.MachineConfigSource{}, nil, err
	}
	retained = pin
	return source, pin, nil
}

// PinSourceConfig pins the actual existing source or selected absent-path
// parent. It does not create a directory or read any configuration payload.
// Unused is accepted only as data; its direct-endpoint provenance must already
// have been checked by the actual closed producer/inspector.
func PinSourceConfig(source client.MachineConfigSource) (_ *SourceConfigPin, err error) {
	pin := &SourceConfigPin{source: source}
	defer func() {
		if err != nil {
			err = errors.Join(err, pin.Close())
		}
	}()
	if source.Role == client.MachineConfigUnused {
		if source.Path != "" || source.ObjectID != "" || source.ParentID != "" {
			return nil, fmt.Errorf("unused provider configuration has an invented source")
		}
		return pin, nil
	}
	if !filepath.IsAbs(source.Path) || filepath.Clean(source.Path) != source.Path {
		return nil, fmt.Errorf("provider configuration source is not absolute and canonical")
	}
	switch source.Role {
	case client.MachineConfigExisting:
		if source.ObjectID == "" || source.ParentID != "" {
			return nil, fmt.Errorf("existing provider configuration has no observed identity")
		}
		pin.file, err = pinSourceConfigDirectory(source.Path)
		if err == nil {
			err = sameSourceConfigIdentity(pin.file, source.ObjectID)
		}
	case client.MachineConfigAbsent:
		if source.ParentID == "" || source.ObjectID != "" || filepath.Dir(source.Path) == source.Path {
			return nil, fmt.Errorf("absent provider configuration has no parent witness")
		}
		pin.file, err = pinSourceConfigDirectory(filepath.Dir(source.Path))
		if err == nil {
			err = sameSourceConfigIdentity(pin.file, source.ParentID)
		}
		if err == nil {
			pin.root, err = os.OpenRoot(filepath.Dir(source.Path))
		}
		if err == nil {
			opened, openErr := pin.root.Open(".")
			if openErr != nil {
				err = openErr
			} else {
				err = errors.Join(sameSourceConfigIdentity(opened, source.ParentID), opened.Close())
			}
		}
		if err == nil {
			_, leafErr := pin.root.Lstat(filepath.Base(source.Path))
			if !errors.Is(leafErr, os.ErrNotExist) {
				err = fmt.Errorf("the selected absent provider configuration now exists or cannot be checked")
			}
		}
	default:
		err = fmt.Errorf("provider configuration source role is unknown")
	}
	if err != nil {
		return nil, fmt.Errorf("pin original provider configuration metadata: %w", err)
	}
	return pin, nil
}

func pinSourceConfigDirectory(path string) (*os.File, error) {
	// This is a read-only native configuration-object witness, not a host
	// mount capability. OpenRoot pins the selected directory on every laptop
	// platform, including Windows; Identity rechecks the original full ID.
	// Payload reads use a no-follow reader relative to the SAME directory.
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(".")
	if closeErr := root.Close(); closeErr != nil {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		return nil, errors.Join(err, closeErr)
	}
	return file, err
}

func sameSourceConfigIdentity(file *os.File, expected string) error {
	identity, err := files.Identity(file)
	if err != nil {
		return fmt.Errorf("read provider configuration identity: %w", err)
	}
	if identity != expected {
		return fmt.Errorf("the provider configuration identity changed")
	}
	return nil
}

// ObserveCreatedDirectory is only a native metadata transition under a held
// selected-absent parent. It creates nothing and grants no birth/sharing fact.
// A closed provider may keep the new directory pinned across its actual Create.
func (p *SourceConfigPin) ObserveCreatedDirectory() (_ client.MachineConfigSource, _ *SourceConfigPin, err error) {
	if p == nil {
		return client.MachineConfigSource{}, nil, fmt.Errorf("the original configuration parent is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.source.Role != client.MachineConfigAbsent {
		return client.MachineConfigSource{}, nil, fmt.Errorf("the original configuration was not an owned absent selection")
	}
	parent, err := pinSourceConfigDirectory(filepath.Dir(p.source.Path))
	if err != nil {
		return client.MachineConfigSource{}, nil, err
	}
	if err = errors.Join(sameSourceConfigIdentity(parent, p.source.ParentID), parent.Close()); err != nil {
		return client.MachineConfigSource{}, nil, err
	}
	source, pin, err := CaptureSourceConfigPinned(p.source.Path)
	if err != nil {
		return client.MachineConfigSource{}, nil, err
	}
	if source.Role != client.MachineConfigExisting || source.Path != p.source.Path {
		return client.MachineConfigSource{}, nil, errors.Join(fmt.Errorf("the created configuration leaves its original selected parent"), pin.Close())
	}
	parent, err = pinSourceConfigDirectory(filepath.Dir(p.source.Path))
	if err != nil {
		return client.MachineConfigSource{}, nil, errors.Join(err, pin.Close())
	}
	if err = errors.Join(sameSourceConfigIdentity(parent, p.source.ParentID), parent.Close()); err != nil {
		return client.MachineConfigSource{}, nil, errors.Join(err, pin.Close())
	}
	return source, pin, nil
}

// RevalidateParent retains the original selected-absent parent witness even
// after the closed provider has observed its newly created child directory.
func (p *SourceConfigPin) RevalidateParent() (err error) {
	if p == nil {
		return fmt.Errorf("the original selected configuration parent is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.source.Role != client.MachineConfigAbsent {
		return fmt.Errorf("the original selected configuration parent is closed or unknown")
	}
	parent, err := pinSourceConfigDirectory(filepath.Dir(p.source.Path))
	if err != nil {
		return err
	}
	return errors.Join(sameSourceConfigIdentity(parent, p.source.ParentID), parent.Close())
}

// Revalidate reacquires the SAME native path/object/absence witness. A changed
// source is an error, not renewal of the old fact by a matching host name.
func (p *SourceConfigPin) Revalidate() error {
	if p == nil {
		return fmt.Errorf("the provider configuration pin is closed")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("the provider configuration pin is closed")
	}
	fresh, err := PinSourceConfig(p.source)
	if err != nil {
		return err
	}
	return fresh.Close()
}

// Close joins closure of only this operation's read-only native metadata pins.
// The caller keeps them until its actual native source operation has joined.
func (p *SourceConfigPin) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var failures []error
	if p.root != nil {
		failures = append(failures, p.root.Close())
	}
	if p.file != nil {
		failures = append(failures, p.file.Close())
	}
	return errors.Join(failures...)
}
