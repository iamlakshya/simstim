//go:build linux

package slotlaunch

import (
	"context"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/jail"
)

func prepareKeeperDisk(plan keeperPlan, directory, disk *os.File) error {
	st, err := disk.Stat()
	if err != nil || !singleRegular(st, os.Getuid()) || st.Size() != plan.Request.DiskMaxGiB*(1<<30) {
		return refused("the held session disk differs from the root allocation")
	}
	if plan.FreshDisk {
		program, binds, err := formatterBinds("/usr/sbin/mkfs.ext4")
		if err != nil {
			return err
		}
		binds = append(binds, jail.Bind{File: directory, Path: RootPath() + "/vms/" + plan.Request.Session, Write: true}, jail.Bind{Path: "/dev/urandom"})
		command, err := jail.Command(context.Background(), jail.Spec{Argv: []string{program, "-t", "ext4", "-q", "-F", "-E", "root_owner=1000:1000", "/proc/self/fd/3"}, Env: []string{"LANG=C", "LC_ALL=C"}, Files: []*os.File{disk}, Binds: binds, Net: jail.NetNone, Seccomp: jail.ProfileConverter})
		if err != nil {
			return err
		}
		if err := command.Start(); err != nil {
			if !command.PhysicalExitV1() {
				return retainKeeper(command, nil)
			}
			return err
		}
		if err := command.Wait(); err != nil {
			if !command.PhysicalExitV1() {
				return retainKeeper(command, nil)
			}
			return err
		}
		if err := disk.Sync(); err != nil {
			return err
		}
	}
	var magic [2]byte
	if _, err := disk.ReadAt(magic[:], 1024+56); err != nil || magic != [2]byte{0x53, 0xef} {
		return refused("the session disk is not an existing ext4 filesystem; it was left unchanged")
	}
	return nil
}

// The formatter consumes only a new empty disk. Its additional read-only
// system program/loader/library binds never enter the final VMM jail.
func formatterBinds(selected string) (string, []jail.Bind, error) {
	if selected == "" {
		var err error
		selected, err = exec.LookPath("mkfs.ext4")
		if err != nil {
			return "", nil, fmt.Errorf("find the host ext4 formatter: %w", err)
		}
	}
	selected, err := filepath.Abs(selected)
	if err != nil {
		return "", nil, fmt.Errorf("resolve host ext4 formatter: %w", err)
	}
	if err := rootProgramFile(selected, true); err != nil {
		return "", nil, err
	}
	triplet := "x86_64-linux-gnu"
	if runtime.GOARCH == "arm64" {
		triplet = "aarch64-linux-gnu"
	}
	defaults := []string{"/lib/" + triplet, "/usr/lib/" + triplet, "/lib64", "/usr/lib64", "/lib", "/usr/lib"}
	seen := map[string]bool{}
	var collect func(string, []string) error
	collect = func(path string, search []string) error {
		if seen[path] {
			return nil
		}
		if err := rootProgramFile(path, false); err != nil {
			return err
		}
		seen[path] = true
		file, err := elf.Open(path)
		if err != nil {
			return fmt.Errorf("inspect host formatter ELF %s: %w", path, err)
		}
		defer file.Close()
		for _, program := range file.Progs {
			if program.Type == elf.PT_INTERP {
				data, err := ioReadProgram(program)
				if err != nil {
					return err
				}
				if err := collect(strings.TrimRight(string(data), "\x00"), defaults); err != nil {
					return err
				}
			}
		}
		paths := append([]string{}, search...)
		for _, tag := range []elf.DynTag{elf.DT_RUNPATH, elf.DT_RPATH} {
			values, _ := file.DynString(tag)
			for _, value := range values {
				for _, dir := range strings.Split(value, ":") {
					dir = strings.ReplaceAll(strings.ReplaceAll(dir, "${ORIGIN}", filepath.Dir(path)), "$ORIGIN", filepath.Dir(path))
					if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
						return fmt.Errorf("host formatter has an untrusted loader search path")
					}
					paths = append(paths, dir)
				}
			}
		}
		paths = append(paths, defaults...)
		libraries, err := file.ImportedLibraries()
		if err != nil {
			return fmt.Errorf("read host formatter libraries: %w", err)
		}
		for _, library := range libraries {
			if filepath.Base(library) != library {
				return fmt.Errorf("host formatter has an unexpected library name")
			}
			found := ""
			for _, dir := range paths {
				candidate := filepath.Join(dir, library)
				if _, err := os.Stat(candidate); err == nil {
					found = candidate
					break
				}
			}
			if found == "" {
				return fmt.Errorf("host formatter library %s is missing", library)
			}
			if err := collect(found, paths); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(selected, defaults); err != nil {
		return "", nil, err
	}
	for _, config := range []string{"/etc/mke2fs.conf", "/etc/ld.so.cache"} {
		if _, err := os.Stat(config); err == nil {
			if err := rootProgramFile(config, false); err != nil {
				return "", nil, err
			}
			seen[config] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	binds := make([]jail.Bind, 0, len(names))
	for _, name := range names {
		binds = append(binds, jail.Bind{Path: name})
	}
	return selected, binds, nil
}

func rootProgramFile(path string, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return fmt.Errorf("host formatter dependency path is not absolute and clean")
	}
	// Check both the alias path and its resolved path. A root-owned target
	// reached through a user's replaceable PATH directory is not trusted.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve host formatter dependency: %w", err)
	}
	for _, name := range []string{path, resolved} {
		for dir := filepath.Dir(name); ; dir = filepath.Dir(dir) {
			info, err := os.Stat(dir)
			if err != nil {
				return fmt.Errorf("inspect host formatter parent: %w", err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("host formatter dependencies need root-owned parent directories without group or other write access")
			}
			if dir == "/" {
				break
			}
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open host formatter dependency: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat host formatter dependency: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 || executable && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("host formatter dependencies must be regular root-owned system files without group or other write access")
	}
	return nil
}

func ioReadProgram(program *elf.Prog) ([]byte, error) {
	if program.Filesz == 0 || program.Filesz > 4096 {
		return nil, fmt.Errorf("host formatter interpreter path exceeds its bound")
	}
	data := make([]byte, int(program.Filesz))
	if _, err := program.ReadAt(data, 0); err != nil {
		return nil, fmt.Errorf("read host formatter interpreter: %w", err)
	}
	return data, nil
}
