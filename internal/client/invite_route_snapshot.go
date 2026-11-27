package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The resolver never opens mutable originals. Only bytes which passed the
// complete preflight are copied here, with Includes replaced by the captured
// ordered closure. Native OpenSSH still decides Host matching and defaults.
type routeConfigSnapshotV1 struct {
	directory string
	info      os.FileInfo
	root      *os.Root
	files     []*routeProjectionFileV1
	wrapper   string
	closed    bool
}

func routeIncludePatternV1(pattern string) (string, error) {
	if strings.ContainsAny(pattern, "%$\x00\r\n") {
		return "", routeSourceErrorV1("an Include has unsupported dynamic expansion")
	}
	if strings.HasPrefix(pattern, "~/") {
		pattern = filepath.Join(home(), pattern[2:])
	} else if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(home(), ".ssh", pattern)
	}
	return filepath.Clean(pattern), nil
}

func freezeRouteConfigV1(ctx context.Context, source *explicitRouteReadsetV1) (result *routeConfigSnapshotV1, failure error) {
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "joined-config-")
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	snapshot := &routeConfigSnapshotV1{directory: directory, root: root, info: info}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, snapshot.close())
		}
	}()
	names := map[string]string{}
	for index, file := range source.files {
		names[file.path] = fmt.Sprintf("source-%d.conf", index)
	}
	for _, file := range source.files {
		var body strings.Builder
		scanner := bufio.NewScanner(bytes.NewReader(file.data))
		scanner.Buffer(make([]byte, 4096), routeConfigFileLimitV1)
		for scanner.Scan() {
			key, args, err := routeConfigLineV1(scanner.Text())
			if err != nil {
				return nil, err
			}
			if key != "include" {
				body.WriteString(scanner.Text())
				body.WriteByte('\n')
				continue
			}
			for _, arg := range args {
				pattern, err := routeIncludePatternV1(arg)
				if err != nil {
					return nil, err
				}
				found := false
				for _, glob := range source.globs {
					if glob.pattern == pattern {
						found = true
						for _, match := range glob.matches {
							name, ok := names[match]
							if !ok {
								return nil, routeSourceErrorV1("a frozen Include is outside its actual captured closure")
							}
							fmt.Fprintf(&body, "Include %q\n", filepath.Join(directory, name))
						}
						break
					}
				}
				if !found {
					return nil, routeSourceErrorV1("a frozen Include lost its original preflight")
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		if err := snapshot.write(names[file.path], []byte(body.String())); err != nil {
			return nil, err
		}
	}
	var wrapper strings.Builder
	for _, option := range noForwarding {
		fmt.Fprintf(&wrapper, "%s %s\n", option[0], option[1])
	}
	wrapper.WriteString("ClearAllForwardings yes\nCanonicalizeHostname no\nPermitLocalCommand no\n")
	for _, path := range source.roots {
		fmt.Fprintf(&wrapper, "Include %q\n", filepath.Join(directory, names[path]))
	}
	if err := snapshot.write("resolve.conf", []byte(wrapper.String())); err != nil {
		return nil, err
	}
	snapshot.wrapper = filepath.Join(directory, "resolve.conf")
	if err := source.check(ctx); err != nil {
		return nil, err
	}
	if err := snapshot.check(ctx); err != nil {
		return nil, err
	}
	keep = true
	return snapshot, nil
}
func (s *routeConfigSnapshotV1) write(name string, data []byte) error {
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	s.files = append(s.files, &routeProjectionFileV1{path: filepath.Join(s.directory, name), info: info, data: append([]byte(nil), data...), directoryInfo: s.info})
	_, err = file.Write(data)
	return errors.Join(err, file.Close())
}
func (s *routeConfigSnapshotV1) check(ctx context.Context) error {
	if s == nil || s.closed {
		return routeSourceErrorV1("the frozen route resolver source ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := os.Lstat(s.directory)
	if err != nil || !os.SameFile(current, s.info) {
		return routeSourceErrorV1("the original frozen config directory changed")
	}
	for _, file := range s.files {
		if file.info == nil {
			continue
		}
		info, err := s.root.Lstat(filepath.Base(file.path))
		if err != nil || !os.SameFile(info, file.info) {
			return routeSourceErrorV1("the original frozen config publisher changed")
		}
		data, err := s.root.ReadFile(filepath.Base(file.path))
		if err != nil || !bytes.Equal(data, file.data) {
			return routeSourceErrorV1("the original frozen config bytes changed")
		}
	}
	return nil
}
func (s *routeConfigSnapshotV1) close() error {
	if s == nil || s.closed {
		return nil
	}
	if err := s.check(context.Background()); err != nil {
		return err
	}
	var failure error
	for _, file := range s.files {
		if file.info == nil {
			continue
		}
		if err := s.root.Remove(filepath.Base(file.path)); err != nil {
			failure = errors.Join(failure, err)
		} else {
			file.info = nil
		}
	}
	if failure == nil {
		failure = os.Remove(s.directory)
	}
	if failure == nil {
		failure = s.root.Close()
	}
	if failure == nil {
		s.closed = true
	}
	return failure
}
