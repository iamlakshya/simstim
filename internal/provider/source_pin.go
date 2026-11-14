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
	absolute, err := filepath.Abs(path)
	if err != nil {
		return client.MachineConfigSource{}, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		file, err := pinSourceConfigDirectory(resolved)
		if err != nil {
			return client.MachineConfigSource{}, err
		}
		identity, err := files.Identity(file)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return client.MachineConfigSource{}, errors.Join(err, closeErr)
		}
		return client.MachineConfigSource{Role: client.MachineConfigExisting, Path: resolved, ObjectID: identity}, nil
	} else if !errors.Is(resolveErr, os.ErrNotExist) {
		return client.MachineConfigSource{}, resolveErr
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return client.MachineConfigSource{}, err
	}
	file, err := pinSourceConfigDirectory(parent)
	if err != nil {
		return client.MachineConfigSource{}, err
	}
	identity, err := files.Identity(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return client.MachineConfigSource{}, errors.Join(err, closeErr)
	}
	source := client.MachineConfigSource{Role: client.MachineConfigAbsent, Path: filepath.Join(parent, filepath.Base(absolute)), ParentID: identity}
	pin, err := PinSourceConfig(source)
	if err != nil {
		return client.MachineConfigSource{}, err
	}
	return source, pin.Close()
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
