//go:build linux

package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func imageVerifierCommand(ctx context.Context, stage *imageVerifierStage) (_ *exec.Cmd, _ func() error, returned error) {
	if err := stage.check(ctx); err != nil {
		return nil, nil, err
	}
	fd, err := unix.MemfdCreate("image-verifier", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING|unix.MFD_EXEC)
	if errors.Is(err, unix.EINVAL) {
		fd, err = unix.MemfdCreate("image-verifier", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	}
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), "image-verifier")
	defer func() {
		if returned != nil {
			file.Close()
		}
	}()
	if _, err := stage.file.Seek(0, 0); err != nil {
		return nil, nil, err
	}
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(imageVerifierCopyReader{ctx: ctx, file: stage.file}, stage.item.Size+1))
	if err != nil {
		return nil, nil, err
	}
	if count != stage.item.Size || hex.EncodeToString(hash.Sum(nil)) != stage.item.SHA256 {
		return nil, nil, fmt.Errorf("the offline image verifier changed while its executable was sealed")
	}
	if err := file.Chmod(0o500); err != nil {
		return nil, nil, err
	}
	const seals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return nil, nil, err
	}
	got, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || got&seals != seals {
		return nil, nil, fmt.Errorf("the offline image verifier has no immutable executable seals")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	command := exec.CommandContext(ctx, "/proc/self/fd/3", "--image-signature-v1")
	command.ExtraFiles = []*os.File{file}
	return command, file.Close, nil
}

type imageVerifierCopyReader struct {
	ctx  context.Context
	file *os.File
}

func (reader imageVerifierCopyReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := reader.file.Read(data)
	if cancelled := reader.ctx.Err(); cancelled != nil {
		return count, cancelled
	}
	return count, err
}
