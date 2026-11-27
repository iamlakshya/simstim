//go:build linux

package process

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/proto"
)

func copyRefusal(message string, cause error) error {
	if errors.Is(cause, unix.EEXIST) {
		return &proto.Error{Code: "cp_exists", Message: "the copy destination already exists", Hint: "Choose another path, or explicitly use --force to replace the file."}
	}
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &proto.Error{Code: "cp_path", Message: message, Hint: "Check the running sandbox and destination, then review the copy again."}
}
func (owner *CopyOwnerV1) Receive(ctx context.Context, header proto.FilePutHeaderV1, input io.Reader) (string, error) {
	if owner == nil || owner.native == nil || ctx == nil || input == nil {
		return "", copyRefusal("the copy has no actual original owner", nil)
	}
	top, relative, err := filecopy.GuestPath(header.Path)
	if err != nil || relative == "." || header.Version != proto.FileTransferVersionV1 || header.Directory || header.Path == "" {
		return "", copyRefusal("the file copy declaration is invalid", err)
	}
	target := path.Join(top, relative)
	native := owner.native
	native.mu.Lock()
	defer native.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(target) >= 4096 {
		return "", copyRefusal("the copy path exceeds its native bound", nil)
	}
	if native.closed.Load() || !native.ready.Load() || native.active != nil {
		return "", copyRefusal("the original process copy owner is closed or busy", nil)
	}
	native.runtime.mu.Lock()
	live := !native.runtime.closed && native.runtime.pidfd >= 0 && native.runtime.copy == owner
	native.runtime.mu.Unlock()
	if !live {
		return "", copyRefusal("the original process runtime ended", nil)
	}
	request := make([]byte, 6+len(target))
	request[0] = 'B'
	if header.Replace {
		request[1] = 1
	}
	binary.LittleEndian.PutUint32(request[2:6], uint32(len(target)))
	copy(request[6:], target)
	if err := copyWrite(native.conn, request); err != nil {
		return "", native.uncertain(err)
	}
	received := make(chan error, 1)
	go func() { received <- native.receiveStage() }()
	if err := native.joinOperation(ctx, received); err != nil {
		return "", copyRefusal("the original file stage could not be prepared", err)
	}
	if native.active == nil {
		return "", native.uncertain(fmt.Errorf("the factory returned no owned stage"))
	}
	err = filecopy.ReceivePreparedStage(ctx, native.active.file, header.Size, header.SHA256, input)
	if err != nil {
		if cancelErr := copyWrite(native.conn, []byte{'X'}); cancelErr != nil {
			return "", errors.Join(err, native.uncertain(cancelErr))
		}
		if cancelErr := copyStatus(native.conn); cancelErr != nil {
			return "", errors.Join(err, native.uncertain(cancelErr))
		}
		return "", errors.Join(err, native.finishStage())
	}
	if err := copyWrite(native.conn, []byte{'C'}); err != nil {
		return "", native.uncertain(err)
	}
	completed := make(chan error, 1)
	go func() { completed <- native.receiveCommit() }()
	err = native.joinOperation(ctx, completed)
	if err != nil {
		// An acknowledged syscall error is still a joined transaction. A physical
		// channel failure retains the exact witness for nativeExit cleanup.
		if !native.closed.Load() {
			err = errors.Join(err, native.finishStage())
		}
		return "", copyRefusal("the original owner did not confirm durable publication", err)
	}
	if err = native.finishStage(); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return target, nil
}
func (o *copyOwnerNativeV1) receiveStage() error {
	data, files, err := copyMessage(o.conn, 1, 1)
	if err != nil {
		return o.uncertain(err)
	}
	if data[0] == 'E' && len(files) == 0 {
		var code [4]byte
		if err := copyOrdinaryRead(o.conn, code[:]); err != nil {
			return o.uncertain(err)
		}
		return unix.Errno(binary.LittleEndian.Uint32(code[:]))
	}
	if data[0] != 'S' || len(files) != 1 {
		for _, file := range files {
			_ = file.Close()
		}
		return o.uncertain(fmt.Errorf("invalid original stage response"))
	}
	var detail [52]byte
	if err := copyOrdinaryRead(o.conn, detail[:]); err != nil {
		_ = files[0].Close()
		return o.uncertain(err)
	}
	mount := o.mounts[binary.LittleEndian.Uint32(detail[:4])]
	nameBytes := detail[4:]
	end := 0
	for end < len(nameBytes) && nameBytes[end] != 0 {
		end++
	}
	if mount == nil || end == len(nameBytes) || end < 6 || !strings.HasPrefix(string(nameBytes[:end]), "file-") {
		_ = files[0].Close()
		return o.uncertain(fmt.Errorf("invalid original stage identity"))
	}
	for _, digit := range nameBytes[5:end] {
		if digit < '0' || digit > '9' {
			_ = files[0].Close()
			return o.uncertain(fmt.Errorf("invalid original stage counter"))
		}
	}
	for _, padding := range nameBytes[end:] {
		if padding != 0 {
			_ = files[0].Close()
			return o.uncertain(fmt.Errorf("invalid original stage name padding"))
		}
	}
	info, err := files[0].Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != 0600 {
		_ = files[0].Close()
		return o.uncertain(errors.Join(err, fmt.Errorf("invalid original stage inode")))
	}
	o.active = &copyStageNativeV1{file: files[0], mount: mount, name: string(nameBytes[:end]), identity: info}
	return nil
}
func (o *copyOwnerNativeV1) receiveCommit() error {
	for {
		data, files, err := copyMessage(o.conn, 1, 1)
		if err != nil {
			return o.uncertain(err)
		}
		switch data[0] {
		case 'P':
			if len(files) != 1 || o.publisher >= 0 {
				for _, file := range files {
					_ = file.Close()
				}
				return o.uncertain(fmt.Errorf("invalid original publisher ownership"))
			}
			raw, err := files[0].SyscallConn()
			duplicate := -1
			var duplicateErr error
			if err == nil {
				err = raw.Control(func(fd uintptr) { duplicate, duplicateErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0) })
			}
			err = errors.Join(err, duplicateErr)
			closeErr := files[0].Close()
			if err != nil || closeErr != nil {
				if duplicate >= 0 {
					_ = unix.Close(duplicate)
				}
				return o.uncertain(errors.Join(err, closeErr))
			}
			if err := signalInit(duplicate, 0, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
				_ = unix.Close(duplicate)
				return o.uncertain(err)
			}
			o.publisher = duplicate
			if err := copyWrite(o.conn, []byte{'p'}); err != nil {
				return o.uncertain(err)
			}
		case 'A':
			if len(files) != 0 {
				for _, file := range files {
					_ = file.Close()
				}
				return o.uncertain(fmt.Errorf("completion carried an unexpected descriptor"))
			}
			var code [4]byte
			if err := copyOrdinaryRead(o.conn, code[:]); err != nil {
				return o.uncertain(err)
			}
			if o.publisher >= 0 {
				if err := waitInitExit(o.publisher); err != nil {
					return o.uncertain(err)
				}
				if err := unix.Close(o.publisher); err != nil {
					return o.uncertain(err)
				}
				o.publisher = -1
			}
			number := binary.LittleEndian.Uint32(code[:])
			if number != 0 {
				return unix.Errno(number)
			}
			return nil
		default:
			for _, file := range files {
				_ = file.Close()
			}
			return o.uncertain(fmt.Errorf("invalid original publisher completion"))
		}
	}
}

// joinOperation never abandons its physical reader. A cancel requests X on the
// SAME original channel, then joins both the current operation and its X ACK.
func (o *copyOwnerNativeV1) joinOperation(ctx context.Context, done <-chan error) error {
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		cancelErr := copyWrite(o.conn, []byte{'X'})
		if cancelErr != nil {
			o.interrupt()
		}
		result := <-done
		if cancelErr == nil {
			cancelErr = copyStatus(o.conn)
			if cancelErr != nil {
				cancelErr = o.uncertain(cancelErr)
			}
		}
		if cancelErr == nil && !o.closed.Load() {
			cancelErr = o.finishStage()
		}
		return errors.Join(ctx.Err(), result, cancelErr)
	}
}
func (o *copyOwnerNativeV1) finishStage() error {
	if o.active == nil {
		return nil
	}
	err := o.active.file.Close()
	o.active = nil
	return err
}
func (o *copyOwnerNativeV1) uncertain(cause error) error {
	o.interrupt()
	o.runtime.mu.Lock()
	o.runtime.result.Err = errors.Join(o.runtime.result.Err, copyRefusal("the original copy owner ended with uncertain resources", cause))
	o.runtime.mu.Unlock()
	return errors.Join(cause, o.runtime.Kill())
}
