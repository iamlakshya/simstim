//go:build linux

package guest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/api"
)

const inputSeals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL

// sealInputBundle uses the same memfd/seal primitive as the runtime owner.
// Only one protected bundle is resident at a time. The copied bytes, rather
// than a mutable ordinal path or an earlier hash, are the Git input authority.
func sealInputBundle(ctx context.Context, source *os.File, repository api.InputRepository, seedMax int64) (_ *os.File, err error) {
	if source == nil || repository.Size <= 0 || seedMax <= 0 || repository.Size > seedMax || repository.Size == math.MaxInt64 {
		return nil, inputError("invalid protected seed size")
	}
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != repository.Size {
		return nil, inputError("the staged seed size changed")
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("input-bundle", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING|unix.MFD_NOEXEC_SEAL)
	if err != nil {
		return nil, err
	}
	sealed := os.NewFile(uintptr(fd), "sealed-input-bundle")
	defer func() {
		if err != nil {
			err = errors.Join(err, sealed.Close())
		}
	}()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(sealed, sum), io.LimitReader(&inputSnapshotReader{ctx: ctx, source: source}, repository.Size+1))
	if err != nil {
		return nil, err
	}
	if n != repository.Size || hex.EncodeToString(sum.Sum(nil)) != repository.SHA256 {
		return nil, inputError("the copied seed differs from its authenticated hash")
	}
	if err = sealed.Chmod(0o444); err != nil {
		return nil, err
	}
	if _, err = unix.FcntlInt(sealed.Fd(), unix.F_ADD_SEALS, inputSeals); err != nil {
		return nil, err
	}
	seals, err := unix.FcntlInt(sealed.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&inputSeals != inputSeals {
		return nil, inputError("the seed snapshot is not immutable")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if _, err = sealed.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return sealed, nil
}

type inputSnapshotReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *inputSnapshotReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}
