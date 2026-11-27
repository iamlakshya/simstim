//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"
)

func (m *vmMemory) dockerGroups(root *os.Root, id string) error {
	for _, directory := range []string{id, filepath.Join(id, "containers")} {
		if directory != id {
			if err := root.Mkdir(directory, 0o755); err != nil {
				return err
			}
		}
		for _, setting := range []struct{ name, value string }{{"memory.oom.group", "0"}, {"cgroup.subtree_control", "+memory"}} {
			if err := m.write(filepath.Join(m.root, "agents", directory, setting.name), []byte(setting.value), 0); err != nil {
				return err
			}
		}
	}
	daemon := filepath.Join(id, "daemon")
	if err := root.Mkdir(daemon, 0o755); err != nil {
		return err
	}
	return m.write(filepath.Join(m.root, "agents", daemon, "memory.oom.group"), []byte("1"), 0)
}

// removeVMGroupDirectories removes only this private, empty cgroup subtree.
// Kernel control files are virtual; each directory disappears via rmdir. The
// caller has already observed populated0 after its owned recursive kill/wait.
func removeVMGroupDirectories(root *os.Root, name string, remove func(string) error) error {
	directory, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	file, err := directory.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("inspect completed guest cgroup")
	}
	for _, entry := range entries {
		info, err := directory.Lstat(entry.Name())
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return vmError("the owned guest cgroup contains a path alias")
		}
		if !info.IsDir() {
			continue
		}
		if err := removeVMGroupDirectories(root, filepath.Join(name, entry.Name()), remove); err != nil {
			return err
		}
	}
	if err := directory.Close(); err != nil {
		return err
	}
	return remove(name)
}
