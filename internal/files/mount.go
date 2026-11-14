package files

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/proto"
)

func mountError(code, message string) error {
	return &proto.Error{Code: code, Message: message, Hint: "Use a read-only mount, or work on the sandbox's clone and pull its changes."}
}

// ParseMount reads host[:guest][:ro|rw]. Paths are literal, without shell
// expansion. A leading ~/ in the host path uses the trusted login home.
func ParseMount(value, home string) (Mount, error) {
	parts := strings.Split(value, ":")
	if len(parts) > 3 || len(parts) == 0 {
		return Mount{}, mountError("bad_mount", "a mount has too many parts")
	}
	m := Mount{}
	if len(parts) > 1 && (parts[len(parts)-1] == "ro" || parts[len(parts)-1] == "rw") {
		m.Write = parts[len(parts)-1] == "rw"
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 2 || parts[0] == "" {
		return m, mountError("bad_mount", "a mount needs a host path")
	}
	m.Host = parts[0]
	if strings.HasPrefix(m.Host, "~/") {
		m.Host = path.Join(home, strings.TrimPrefix(m.Host, "~/"))
	}
	if !path.IsAbs(m.Host) || path.Clean(m.Host) != m.Host || !textPath(m.Host) {
		return m, mountError("bad_mount", "the host mount path must be absolute and clean")
	}
	m.Guest = "/mnt/" + path.Base(m.Host)
	if len(parts) == 2 {
		m.Guest = parts[1]
	}
	if strings.HasPrefix(m.Guest, "~/") {
		m.Guest = "/home/agent/" + strings.TrimPrefix(m.Guest, "~/")
	}
	if !GuestPath(m.Guest) {
		return m, mountError("bad_mount", "the guest mount path is reserved or is not absolute and clean")
	}
	return m, nil
}

func textPath(p string) bool {
	if len(p) >= 4096 || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// GuestPath reports whether a mount destination avoids kernel and runtime
// roots. The driver also checks overlap with system, work and state binds.
func GuestPath(p string) bool {
	if p == "/" || !path.IsAbs(p) || path.Clean(p) != p || !textPath(p) {
		return false
	}
	for _, root := range []string{"/proc", "/dev", "/run", "/root", "/sys", RestorePath, CapturePath} {
		if p == root || strings.HasPrefix(p, root+"/") || strings.HasPrefix(root, p+"/") {
			return false
		}
	}
	return true
}

// CheckMounts rejects duplicate destinations and sources inside private
// session storage before any sandbox exists. PinMount checks the source.
func CheckMounts(mounts []Mount, privateRoots []string) error {
	for i, m := range mounts {
		if !GuestPath(m.Guest) {
			return mountError("bad_mount", "a guest mount path is reserved")
		}
		for _, root := range privateRoots {
			if resolved, err := filepath.EvalSymlinks(root); err == nil {
				root = resolved
			}
			if root != "" && (m.Host == root || strings.HasPrefix(m.Host, root+string(os.PathSeparator)) || strings.HasPrefix(root, m.Host+string(os.PathSeparator))) {
				return mountError("bad_mount", "private session files cannot be mounted")
			}
		}
		for _, old := range mounts[:i] {
			if overlaps(m.Guest, old.Guest) {
				return mountError("bad_mount", "guest mount paths overlap")
			}
		}
		f, err := PinGrant(m)
		if err != nil {
			return err
		}
		f.Close()
	}
	return nil
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// ExplainMount returns the row's direct-folder badge when a grant writes.
func ExplainMount(mounts []Mount) string {
	for _, m := range mounts {
		if m.Write {
			return "direct folder"
		}
	}
	return ""
}
