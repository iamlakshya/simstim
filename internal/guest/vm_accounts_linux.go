//go:build linux

package guest

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/files"
	"golang.org/x/sys/unix"
)

const holderUID, holderGID, dockerGID = 900, 900, 2000
const vmAccountBytes = 1 << 20

func vmAccountRecords(input []byte, group bool) ([]byte, error) {
	if len(input) > vmAccountBytes || !utf8.Valid(input) || bytes.IndexByte(input, 0) >= 0 || bytes.IndexByte(input, '\r') >= 0 {
		return nil, vmError("the image account file is too large or contains invalid text")
	}
	reserved := map[string]int{"root": 0, "holder": holderUID, "agent": AgentUID}
	if group {
		reserved["docker"] = dockerGID
	}
	loginShell := "/bin/sh"
	seen := map[string]bool{}
	var retained []string
	scanner := bufio.NewScanner(bytes.NewReader(input))
	scanner.Buffer(make([]byte, 4096), vmAccountBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			retained = append(retained, line)
			continue
		}
		fields := strings.Split(line, ":")
		wantFields := 7
		if group {
			wantFields = 4
		}
		if len(fields) != wantFields || fields[0] == "" || seen[fields[0]] {
			return nil, vmError("the image has an ambiguous account or group record")
		}
		seen[fields[0]] = true
		id, err := strconv.Atoi(fields[2])
		if err != nil || id < 0 {
			return nil, vmError("the image has an invalid account or group ID")
		}
		if want, ok := reserved[fields[0]]; ok {
			// The reserved docker name is deliberately normalised to2000;
			// no unrelated image file ownership is guessed or changed.
			if !(group && fields[0] == "docker") && id != want {
				return nil, vmError("the image conflicts with a fixed VM account")
			}
			if !group && fields[0] == "agent" {
				if !filepath.IsAbs(fields[6]) {
					return nil, vmError("the image agent login shell is not absolute")
				}
				loginShell = fields[6]
			}
			continue
		}
		for _, want := range reserved {
			if id == want {
				return nil, vmError("the image reuses a reserved VM account or group ID")
			}
		}
		retained = append(retained, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if group {
		retained = append(retained, "root:x:0:", "holder:x:900:", "agent:x:1000:", "docker:x:2000:")
	} else {
		retained = append(retained, "root:x:0:0:root:/root:/bin/sh", "holder:x:900:900:terminal:/nonexistent:/usr/sbin/nologin", "agent:x:1000:1000:agent:/home/agent:"+loginShell)
	}
	return []byte(strings.Join(retained, "\n") + "\n"), nil
}

func vmReadAccount(root *os.Root, name string) ([]byte, error) {
	file, err := files.OpenRegular(root, name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, vmError("the image account data is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, vmAccountBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > vmAccountBytes {
		return nil, vmError("the image account data exceeds its bound")
	}
	return data, nil
}

func (v *vmFiles) accounts(sudo bool) error {
	root, err := os.OpenRoot(v.root)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, record := range []struct {
		name  string
		group bool
	}{{"etc/passwd", false}, {"etc/group", true}} {
		input, err := vmReadAccount(root, record.name)
		if err != nil {
			return err
		}
		data, err := vmAccountRecords(input, record.group)
		if err != nil {
			return err
		}
		if err := vmAtomicFile(root, record.name, data, 0o644); err != nil {
			return err
		}
	}
	sudoers := "root ALL=(ALL:ALL) ALL\n"
	if sudo {
		file, err := root.OpenFile("usr/bin/sudo", os.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			return vmError("sudo=true requires the image's actual sudo executable")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			file.Close()
			return vmError("the image sudo executable is invalid")
		}
		if err := v.ops.own(file, 0, 0); err != nil {
			file.Close()
			return err
		}
		if err := file.Chmod(os.ModeSetuid | 0o755); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		sudoers = "Defaults env_reset\nDefaults secure_path=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nroot ALL=(ALL:ALL) ALL\nagent ALL=(ALL:ALL) NOPASSWD: ALL\n"
	}
	if err := vmAtomicFile(root, "etc/sudoers", []byte(sudoers), 0o440); err != nil {
		return err
	}
	if err := vmAtomicFile(root, "etc/resolv.conf", []byte("nameserver 127.0.0.53\noptions timeout:2 attempts:2\n"), 0o644); err != nil {
		return err
	}
	if err := vmAtomicFile(root, "etc/hosts", []byte("127.0.0.1 localhost\n::1 localhost\n"), 0o644); err != nil {
		return err
	}
	for _, shell := range []string{"bin/sh"} {
		file, err := root.OpenFile(shell, os.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			return vmError("the image has no executable /bin/sh")
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("the image shell is not executable")
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
