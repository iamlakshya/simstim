package host

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func safeName(n string) string {
	n = filepath.Base(n)
	var sb strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	s := strings.TrimLeft(sb.String(), ".")
	if s == "" {
		s = "file"
	}
	if len(s) > 80 {
		s = s[len(s)-80:]
	}
	return s
}

func cleanOld(dir string, age time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > age {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
