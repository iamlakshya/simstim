package host

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/iamlakshya/simstim/internal/version"
	"io"
	"os"
	"path/filepath"
)

func executableWallVersion(exe string) string {
	var f *os.File
	var err error
	if exe == "/proc/self/exe" {
		// This exact kernel link identifies the mapped running executable,
		// including an installation whose old pathname was replaced.
		f, err = os.Open("/proc/self/exe")
	} else {
		if !filepath.IsAbs(exe) || filepath.Clean(exe) != exe {
			return ""
		}
		root, openErr := os.OpenRoot(filepath.Dir(exe))
		if openErr != nil {
			return ""
		}
		defer root.Close()
		f, err = root.Open(filepath.Base(exe))
	}
	if err != nil {
		return ""
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, 512<<20+1))
	if err != nil || n > 512<<20 {
		return ""
	}
	identity := version.Version + "+" + hex.EncodeToString(hash.Sum(nil))
	return identity
}
