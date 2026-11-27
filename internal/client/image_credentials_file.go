package client

import (
	"context"
	"fmt"
	"os"
)

// The registry config is data, read once from its original regular file. A
// special file or a changed final name is refused before any credential read.
func openImageCredentialConfig(path string) (configFile, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("registry login config is not a regular file")
	}
	file, err := openImageCredentialFile(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		file.Close()
		return nil, err
	}
	if !after.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(before, after) || !os.SameFile(after, current) {
		file.Close()
		return nil, fmt.Errorf("registry login config changed while its regular file was opened")
	}
	return file, nil
}

type imageCredentialReader struct {
	ctx  context.Context
	file configFile
}

func (reader imageCredentialReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := reader.file.Read(data)
	if cancelled := reader.ctx.Err(); cancelled != nil {
		return count, cancelled
	}
	return count, err
}
