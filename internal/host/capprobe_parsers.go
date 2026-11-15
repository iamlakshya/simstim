package host

import (
	"encoding/binary"
	"slices"
	"strconv"
	"strings"
)

// probeOSRelease reads the display name without evaluating shell syntax.
func probeOSRelease(b []byte) string {
	values := make(map[string]string)
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		} else if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\$`, `$`, "\\`", "`").Replace(value[1 : len(value)-1])
		}
		values[key] = value
	}
	if values["PRETTY_NAME"] != "" {
		return values["PRETTY_NAME"]
	}
	return strings.TrimSpace(values["NAME"] + " " + values["VERSION_ID"])
}

func probeCPUVirt(b []byte, arch string) string {
	if arch != "amd64" {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "flags" {
			continue
		}
		flags := strings.Fields(value)
		if slices.Contains(flags, "vmx") {
			return "Intel VT-x"
		}
		if slices.Contains(flags, "svm") {
			return "AMD-V"
		}
	}
	return ""
}

func probeControllers(b []byte) []string {
	var controllers []string
	for _, word := range strings.Fields(string(b)) {
		valid := word != ""
		for _, r := range word {
			if r < 'a' || r > 'z' {
				valid = false
			}
		}
		if valid && !slices.Contains(controllers, word) {
			controllers = append(controllers, word)
		}
	}
	slices.Sort(controllers)
	return controllers
}

// probeACLUser checks Linux's version 2 POSIX access ACL, including the mask.
func probeACLUser(b []byte, uid uint32) bool {
	if len(b) < 4 || (len(b)-4)%8 != 0 || binary.LittleEndian.Uint32(b[:4]) != 2 {
		return false
	}
	var named, mask uint16
	found, haveMask := false, false
	for offset := 4; offset < len(b); offset += 8 {
		tag := binary.LittleEndian.Uint16(b[offset : offset+2])
		perm := binary.LittleEndian.Uint16(b[offset+2 : offset+4])
		id := binary.LittleEndian.Uint32(b[offset+4 : offset+8])
		if perm & ^uint16(7) != 0 {
			return false
		}
		switch tag {
		case 2:
			if id == uid {
				if found {
					return false
				}
				named, found = perm, true
			}
		case 16:
			if haveMask {
				return false
			}
			mask, haveMask = perm, true
		}
	}
	return found && haveMask && named&mask&6 == 6
}

func probeStatusGroups(b []byte) []string {
	for _, line := range strings.Split(string(b), "\n") {
		if value, ok := strings.CutPrefix(line, "Groups:"); ok {
			return strings.Fields(value)
		}
	}
	return nil
}

func probeStatusSeccomp(b []byte) bool {
	for _, line := range strings.Split(string(b), "\n") {
		if value, ok := strings.CutPrefix(line, "Seccomp:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			return err == nil && n >= 0 && n <= 2
		}
	}
	return false
}

func probeSubID(b []byte, username string, uid int) bool {
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 3 || (fields[0] != username && fields[0] != strconv.Itoa(uid)) {
			continue
		}
		start, e1 := strconv.ParseUint(fields[1], 10, 32)
		count, e2 := strconv.ParseUint(fields[2], 10, 32)
		if e1 == nil && e2 == nil && start > 0 && count >= 65536 && start+count <= 1<<32 {
			return true
		}
	}
	return false
}

func probeMemTotal(b []byte) int64 {
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "MemTotal:" || fields[2] != "kB" {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && n > 0 && n <= (1<<63-1)/1024 {
			return n * 1024
		}
	}
	return 0
}
