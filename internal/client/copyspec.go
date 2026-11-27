package client

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// CopySource names one local source and its absolute Linux destination.
// Host is local-only and must never enter an API request or audit record.
type CopySource struct {
	Host  string
	Guest string
}

// ParseCopySource reads source[:guest] without shell expansion. windows
// describes the laptop path grammar, not the guest filesystem. A drive colon
// belongs to the source. All other colons delimit one explicit guest path.
func ParseCopySource(value, home string, windows bool) (CopySource, error) {
	bad := func() (CopySource, error) {
		return CopySource{}, envInputError("bad_copy", "a copy needs a local source and an optional absolute guest path", "Use --copy SOURCE or --copy SOURCE:/guest/path.", KindUsage)
	}
	start := 0
	if windows && len(value) >= 2 && asciiDrive(value[0]) && value[1] == ':' {
		start = 2
		if len(value) == 2 || value[2] != '\\' && value[2] != '/' {
			return bad()
		}
	}
	index := strings.IndexByte(value[start:], ':')
	source, guest := value, ""
	if index >= 0 {
		index += start
		source, guest = value[:index], value[index+1:]
		if strings.ContainsRune(guest, ':') || guest == "" {
			return bad()
		}
	}
	if source == "" || strings.ContainsAny(source, "\x00\r\n") {
		return bad()
	}
	if source == "~" {
		source = home
	} else if strings.HasPrefix(source, "~/") || windows && strings.HasPrefix(source, "~\\") {
		source = home + string(filepath.Separator) + source[2:]
	}
	if guest == "~" {
		guest = "/home/agent"
	} else if strings.HasPrefix(guest, "~/") {
		guest = "/home/agent/" + guest[2:]
	}
	if guest != "" && !api.InputGuestPath(guest) {
		return bad()
	}
	return CopySource{Host: source, Guest: guest}, nil
}

func asciiDrive(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func defaultCopyGuest(source, home string) string {
	if rel, err := filepath.Rel(home, source); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path.Join("/home/agent", filepath.ToSlash(rel))
	}
	return path.Join("/mnt", filepath.Base(source))
}
