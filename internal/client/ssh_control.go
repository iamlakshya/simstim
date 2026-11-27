package client

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

const (
	maxSocketPath = 103
	// OpenSSH binds a temporary path with a dot and sixteen random bytes.
	controlBindExtra = 17
	controlPrefix    = "nf-"
	controlKeyLen    = 26
	controlRouteLen  = 40
)

var controlKeyRE = regexp.MustCompile(`^[a-z2-7]{26}$`)
var legacyControlKeyRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
var controlSocketRE = regexp.MustCompile(`^nf-(?:(?:[a-z2-7]{26}|[0-9a-f]{16})-)?[0-9a-f]{40}$`)

// ControlDir returns the current owner-private short base of shared SSH
// connection epochs, or an empty string when sharing is unavailable.
func ControlDir() string {
	if !controlMaster {
		return ""
	}
	return controlDir()
}

func controlName(_ string) string { return controlPrefix + "%C" }

func controlFits(dir string) bool {
	return len(dir)+1+controlKeyLen+1+len(controlName(""))-2+controlRouteLen+controlBindExtra <= maxSocketPath
}

func controlFallback(uid uint32) string {
	return filepath.Join("/tmp", "cm-"+strconv.FormatUint(uint64(uid), 36))
}

var controlFallbackDir = controlFallback

func controlDir() string {
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > math.MaxUint32 {
		return ""
	}
	for _, dir := range []string{filepath.Join(CacheDir(), "cm"), controlFallbackDir(uint32(uid))} {
		if !controlFits(dir) || strings.ContainsAny(dir, "\r\n\x00") {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err == nil && privateDir(dir) {
			return dir
		}
	}
	return ""
}

func controlHash(domain, value string) string {
	scope, _ := controlScope()
	sum := sha256.Sum256([]byte(domain + "\x00" + brand.Name + "\x00" + scope + "\x00" + value))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:16]))
}

func controlHostKey(name string) string { return controlHash("host", name) }

// controlKey holds 128 bits over the exact host and canonical pin set. The
// route's independent %C remains part of the socket name.
func controlKey(name string, keys []hostKey) string {
	fingerprints := make([]string, 0, len(keys))
	for _, key := range keys {
		fingerprints = append(fingerprints, key.Fingerprint())
	}
	slices.Sort(fingerprints)
	fingerprints = slices.Compact(fingerprints)
	pin := "0"
	if len(fingerprints) > 0 {
		pin = "1\x00" + strings.Join(fingerprints, "\x00")
	}
	return controlHash("ckey", name+"\x00"+pin)
}

func legacyControlKey(name string, keys []hostKey) string {
	h := sha256.New()
	h.Write([]byte(name + "\x00"))
	fingerprints := make([]string, 0, len(keys))
	for _, key := range keys {
		fingerprints = append(fingerprints, key.Fingerprint())
	}
	slices.Sort(fingerprints)
	h.Write([]byte(strings.Join(fingerprints, "\x00")))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func controlDirs() []string {
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > math.MaxUint32 {
		return nil
	}
	return slices.Compact([]string{
		filepath.Join(CacheDir(), "cm"),
		controlFallbackDir(uint32(uid)),
	})
}

func retireControls(ctx context.Context, name string) error {
	if !controlMaster || !ValidHostName(name) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return retireControlsAt(ctx, name)
}

func retireControlsAt(ctx context.Context, name string) error {
	for _, dir := range controlDirs() {
		if !privateDir(dir) {
			continue
		}
		if err := retireControlIndex(ctx, dir, name); err != nil {
			return err
		}
	}
	return nil
}

func controlMarkerValid(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && info.Size() <= controlKeyLen+1 && controlFileOwned(info)
}

func retireControlKey(ctx context.Context, dir, key string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), controlPrefix+key+"-") || entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if controlSocketRE.MatchString(entry.Name()) {
			controlExit(ctx, path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			DebugLog().Warn("could not remove a retired SSH socket", "path", path, "err", err)
		}
	}
}

func controlExit(ctx context.Context, path string) {
	path = sshPathArg(strings.ReplaceAll(path, "%", "%%"), runtime.GOOS)
	cmd := exec.CommandContext(ctx, "ssh", "-F", os.DevNull, "-o", "BatchMode=yes", "-o", "ControlPath="+path, "-O", "exit", "--", "placeholder")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = sshWaitDelay
	_ = cmd.Run()
}
