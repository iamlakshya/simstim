package paths

import (
	"path/filepath"
	"strconv"
)

// PortSocket is the private Unix socket for one granted guest port.
func PortSocket(id string, port int) string {
	return filepath.Join(RuntimeDir(), "p", id, strconv.Itoa(port)+".sock")
}
