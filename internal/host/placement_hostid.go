package host

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/paths"
)

func loadHostID() (string, error) {
	dir := paths.StateDir()
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	path := paths.HostID()
	id, err := readHostID(path)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return createHostID(dir, path)
}

func readHostIDDescriptor(file *os.File) (string, error) {
	data, err := io.ReadAll(io.LimitReader(file, 35))
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !ValidHostID(id) {
		return "", UserError("host_id", "the owned host ID does not contain one valid identity", "Keep the host saved and inspect its host-id receipt before retrying.")
	}
	return id, nil
}
