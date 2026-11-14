package client

import (
	"context"
	"errors"
	"io"
	"os"
)

var errAgentSettingsChanged = errors.New("config.toml changed while it was being checked")

type absentAgentSettings struct{}

func (absentAgentSettings) ReadAt([]byte, int64) (int, error) { return 0, io.EOF }

type agentSettingsSnapshot struct {
	ctx            context.Context
	path           string
	file           configFile
	reader         io.ReaderAt
	before         os.FileInfo
	size           int64
	absent, closed bool
}

func openAgentSettingsSnapshot(ctx context.Context) (*agentSettingsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := settingsPath()
	file, err := openConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, linkErr := os.Lstat(path); errors.Is(linkErr, os.ErrNotExist) {
				return &agentSettingsSnapshot{ctx: ctx, path: path, reader: absentAgentSettings{}, absent: true}, nil
			}
			return nil, agentSettingsReadFailure()
		}
		return nil, agentSettingsReadFailure()
	}
	if file == nil {
		return nil, agentSettingsReadFailure()
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, agentSettingsReadFailure()
	}
	if info == nil || !info.Mode().IsRegular() {
		return nil, ErrNotAFile
	}
	if info.Size() < 0 || info.Size() > MaxConfigSize {
		return nil, ErrConfigTooLarge
	}
	reader, ok := file.(io.ReaderAt)
	if !ok {
		return nil, errAgentSettingsSchema
	}
	snapshot := &agentSettingsSnapshot{ctx: ctx, path: path, file: file, reader: reader, before: info, size: info.Size()}
	if err := snapshot.end(); err != nil {
		return nil, err
	}
	keep = true
	return snapshot, nil
}

func (s *agentSettingsSnapshot) end() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.absent {
		return nil
	}
	var tail [1]byte
	n, err := s.reader.ReadAt(tail[:], s.size)
	if stopped := s.ctx.Err(); stopped != nil {
		return stopped
	}
	if n != 0 {
		return errAgentSettingsChanged
	}
	if err != io.EOF {
		if err != nil {
			return agentSettingsReadFailure()
		}
		return io.ErrNoProgress
	}
	return nil
}
func (s *agentSettingsSnapshot) check() error {
	if s == nil || s.closed {
		return errAgentSettingsChanged
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.absent {
		_, err := os.Lstat(s.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errAgentSettingsChanged
	}
	after, err := s.file.Stat()
	if err != nil {
		return agentSettingsReadFailure()
	}
	if after == nil || !after.Mode().IsRegular() || !os.SameFile(s.before, after) || after.Size() != s.size || !after.ModTime().Equal(s.before.ModTime()) || after.Mode() != s.before.Mode() {
		return errAgentSettingsChanged
	}
	// Keep the original namespace identity as well as the original FD. A
	// replaced path cannot silently become the operation's current source.
	named, err := os.Stat(s.path)
	if err != nil || named == nil || !os.SameFile(s.before, named) || named.Size() != s.size || !named.ModTime().Equal(s.before.ModTime()) || named.Mode() != s.before.Mode() {
		return errAgentSettingsChanged
	}
	return s.end()
}
func (s *agentSettingsSnapshot) close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

func agentSettingsReadFailure() error {
	return &UserError{Msg: "config.toml cannot be read", Hint: "Check " + settingsPath() + ".", Code: "config_read", Kind: KindFailed}
}
