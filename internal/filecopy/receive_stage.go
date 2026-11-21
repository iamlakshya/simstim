package filecopy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

// ReceivePreparedStage validates exact declared bytes into an already-open
// empty regular stage. It does not open a path, publish, accept a guest mode,
// construct a commit owner or acquire filesystem authority. The actual runtime
// resource producer retains the descriptor and owns publication/cleanup.
func ReceivePreparedStage(ctx context.Context, file *os.File, size int64, digest string, input io.Reader) (err error) {
	if ctx == nil || file == nil || input == nil || size < 0 || len(digest) != 64 {
		return failure("cp_path", "the prepared copy stage declaration is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return failure("cp_path", "the copy digest is invalid")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 0 || offset != 0 {
		return failure("cp_changed", "the prepared copy stage is not an empty regular file")
	}
	hash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(file, hash), contextReader{ctx, input}, size)
	if err != nil || n != size {
		return errors.Join(failure("cp_too_big", "the copied file differs from its listed size"), err)
	}
	var extra [1]byte
	more, endErr := (contextReader{ctx, input}).Read(extra[:])
	if more != 0 || !errors.Is(endErr, io.EOF) {
		return errors.Join(failure("cp_too_big", "the copied file sends bytes beyond its listed size"), endErr)
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(digest) {
		return failure("cp_changed", "the copied file differs from its checked digest")
	}
	if err = file.Chmod(0644); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}

	return nil
}
