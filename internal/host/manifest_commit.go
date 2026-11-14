package host

import (
	"errors"
	"fmt"
	"os"
)

type manifestDirectory interface {
	Sync() error
	Close() error
}

var openManifestDirectory = func(name string) (manifestDirectory, error) { return os.Open(name) }
var closeManifestTemp = func(file *os.File) error { return file.Close() }

func commitManifestDirectory(directory string) error {
	file, err := openManifestDirectory(directory)
	if err != nil {
		return fmt.Errorf("open the manifest's parent folder for commit: %w", err)
	}
	syncErr, closeErr := file.Sync(), file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("commit the manifest's parent folder: %w", err)
	}
	return nil
}
