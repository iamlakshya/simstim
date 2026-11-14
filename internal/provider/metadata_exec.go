package provider

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type boundedMetadataOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedMetadataOutput) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		b.exceeded = true
		return 0, fmt.Errorf("provider metadata output exceeds its bound")
	}
	return b.Buffer.Write(data)
}

// ExecMetadata uses the SAME provider executor with explicit checked metadata,
// a fixed local inspector tool and bounded output. It never imports a caller's
// ambient environment or current-directory/PATHEXT executable fallback.
func ExecMetadata(ctx context.Context, environment []string, limit int, install, name string, args ...string) ([]byte, error) {
	if environment == nil || limit <= 0 || name != "docker" && name != "limactl" {
		return nil, fmt.Errorf("provider metadata operation has no closed environment")
	}
	path := ""
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			path = value
		}
	}
	leaf := name
	if runtime.GOOS == "windows" {
		leaf += ".exe"
	}
	for _, directory := range filepath.SplitList(path) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, leaf)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			continue
		}
		return execInput(ctx, nil, environment, limit, install, candidate, args...)
	}
	return nil, fmt.Errorf("the fixed provider metadata tool is unavailable")
}
