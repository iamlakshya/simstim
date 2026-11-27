package jail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/wall/process"
	"golang.org/x/sys/unix"
)

// GvisorBootstrapV1 owns the actual native C launcher and mapped Go helper.
// Capture accepts no paths, descriptors, policy or caller identity. This is
// public code, not a session, namespace or private-data authority.
type GvisorBootstrapV1 struct {
	mu               sync.Mutex
	launcher, helper *os.File
	identity         string
	closed           bool
}

// CaptureGvisorBootstrapV1 pins the selected current/AppArmor C object once.
// The exact same held object supplies both cache identity and execution.
func CaptureGvisorBootstrapV1(ctx context.Context) (_ *GvisorBootstrapV1, failureErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	launcher, err := process.OpenGvisorOuterLauncherV1(filepath.Dir(executable))
	if err != nil {
		return nil, err
	}
	helper, err := os.Open(self)
	if err != nil {
		return nil, errors.Join(err, launcher.Close())
	}
	source := &GvisorBootstrapV1{launcher: launcher, helper: helper}
	source.identity, err = gvisorCodeClosureV1(ctx, launcher, helper)
	if err != nil {
		return nil, errors.Join(err, source.Close())
	}
	return source, nil
}

type gvisorCodeRecordV1 struct {
	Role              string
	Dev, Ino          uint64
	Mode, UID, GID    uint32
	Size              int64
	Modified, Changed unix.Timespec
	Digest            string
}

func gvisorCodeRecordFromStatV1(role string, st unix.Stat_t, digest string) gvisorCodeRecordV1 {
	return gvisorCodeRecordV1{Role: role, Dev: uint64(st.Dev), Ino: st.Ino, Mode: st.Mode, UID: st.Uid, GID: st.Gid, Size: st.Size, Modified: st.Mtim, Changed: st.Ctim, Digest: digest}
}

func gvisorCodeClosureV1(ctx context.Context, launcher, helper *os.File) (string, error) {
	records := make([]gvisorCodeRecordV1, 0, 2)
	for index, file := range []*os.File{launcher, helper} {
		if file == nil {
			return "", failure("the native bootstrap lost a held code object", "Capture the original helper and current C launcher again.")
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fd := int(file.Fd())
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
			return "", errors.Join(err, failure("native bootstrap code is not read-only", "Retain the original read-only code descriptors."))
		}
		var before, after unix.Stat_t
		if err := unix.Fstat(fd, &before); err != nil {
			return "", err
		}
		if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0111 == 0 || before.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 || before.Size <= 0 || before.Size > 512<<20 {
			return "", failure("native bootstrap code has an unsupported executable shape", "Use the current static launcher and mapped helper.")
		}
		digest := sha256.New()
		reader := io.NewSectionReader(file, 0, before.Size)
		buffer := make([]byte, 64<<10)
		var total int64
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			n, err := reader.Read(buffer)
			if n > 0 {
				_, _ = digest.Write(buffer[:n])
				total += int64(n)
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", err
			}
		}
		if err := unix.Fstat(fd, &after); err != nil {
			return "", err
		}
		role := []string{"native-c-launcher-v1", "mapped-go-helper-v1"}[index]
		record := gvisorCodeRecordFromStatV1(role, before, "")
		if total != before.Size || record != gvisorCodeRecordFromStatV1(role, after, "") {
			return "", failure("native bootstrap code changed while captured", "Retry with one unchanged helper installation.")
		}
		record.Digest = hex.EncodeToString(digest.Sum(nil))
		records = append(records, record)
	}
	raw, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// IdentityV1 rechecks the same native objects, bytes and metadata. A path
// replacement cannot redirect this source to a different executable.
func (source *GvisorBootstrapV1) IdentityV1(ctx context.Context) (string, error) {
	if source == nil {
		return "", failure("native bootstrap has no original code source", "Capture the actual helper closure before probing.")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.checkLocked(ctx)
}
func (source *GvisorBootstrapV1) checkLocked(ctx context.Context) (string, error) {
	if source.closed || source.identity == "" {
		return "", failure("the native bootstrap code lifetime ended", "Capture a fresh original code source.")
	}
	identity, err := gvisorCodeClosureV1(ctx, source.launcher, source.helper)
	if err != nil {
		return "", err
	}
	if identity != source.identity {
		return "", failure("the held native bootstrap code changed", "Probe the current helper and C launcher again.")
	}
	return identity, nil
}
func (source *GvisorBootstrapV1) retain(ctx context.Context) (_ *os.File, _ *os.File, identity string, err error) {
	if source == nil {
		return nil, nil, "", failure("the native command has no captured bootstrap", "Retain the original code source through command construction.")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	identity, err = source.checkLocked(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	fd, err := unix.FcntlInt(source.launcher.Fd(), unix.F_DUPFD_CLOEXEC, 32)
	if err != nil {
		return nil, nil, "", err
	}
	launcher := os.NewFile(uintptr(fd), "same-original-native-launcher")
	fd, err = unix.FcntlInt(source.helper.Fd(), unix.F_DUPFD_CLOEXEC, 32)
	if err != nil {
		return nil, nil, "", errors.Join(err, launcher.Close())
	}
	helper := os.NewFile(uintptr(fd), "same-original-mapped-helper")
	actual, err := gvisorCodeClosureV1(ctx, launcher, helper)
	if err != nil || actual != identity {
		return nil, nil, "", errors.Join(err, launcher.Close(), helper.Close(), failure("the retained bootstrap differs from the captured code", "Retain one unchanged original code closure."))
	}
	return launcher, helper, identity, nil
}
func (source *GvisorBootstrapV1) Close() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return nil
	}
	source.closed = true
	var result error
	if source.launcher != nil {
		result = errors.Join(result, source.launcher.Close())
	}
	if source.helper != nil {
		result = errors.Join(result, source.helper.Close())
	}
	return result
}
