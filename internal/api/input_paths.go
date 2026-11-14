package api

import (
	"path"
	"strings"
	"unicode/utf8"
)

// InputGuestPath accepts clean absolute Linux destinations outside runtime,
// system and Git metadata. More specific installed/state mounts are checked
// against the protected start plan before a receiver writes any file.
func InputGuestPath(name string) bool {
	if len(name) >= 4096 || !utf8.ValidString(name) || !path.IsAbs(name) || path.Clean(name) != name || name == "/" || strings.ContainsAny(name, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return false
		}
	}
	for _, reserved := range []string{"/proc", "/sys", "/dev", "/run", "/root", "/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc", "/mnt/.agent-state-restore", "/mnt/.agent-state-capture"} {
		if name == reserved || strings.HasPrefix(name, reserved+"/") || strings.HasPrefix(reserved, name+"/") {
			return false
		}
	}
	return true
}

// InputRepoName is one visible, unambiguous directory name below /work.
func InputRepoName(name string) bool {
	return name != "" && name != "." && name != ".." && name != ".git" && !strings.ContainsAny(name, "/\\") && InputGuestPath("/work/"+name) && inputText(name) && CleanText(name, len(name)) == name
}

func inputText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
