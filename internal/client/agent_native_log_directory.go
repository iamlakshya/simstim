package client

import "path/filepath"

func (l *agentNativeLog) borrowDirectory(directory *agentModeDirectory, owns bool) error {
	l.directory, l.ownsDirectory = directory, owns
	if err := directory.check(); err != nil {
		return err
	}
	l.parent, l.root = directory.parent, directory.root
	l.dirName, l.dirInfo = directory.name, directory.info
	l.path = filepath.Join(directory.path, "native.fifo")
	return nil
}
