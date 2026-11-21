package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// These are immutable original-source restrictions, never permission data.
// The actual current registration/saved Source and native joined pairs remain
// mandatory before a stored route can be used.
type joinedRouteFileRefV1 struct {
	Path   string `json:"path"`
	Native string `json:"native"`
	SHA256 string `json:"sha256"`
}
type joinedRouteDirectoryRefV1 struct {
	Path   string `json:"path"`
	Native string `json:"native"`
}
type joinedRouteCredentialRefV1 struct {
	Path     string    `json:"path"`
	Native   string    `json:"native,omitempty"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Socket   bool      `json:"socket,omitempty"`
	Missing  bool      `json:"missing,omitempty"`
}
type joinedRouteGlobRefV1 struct {
	Pattern string   `json:"pattern"`
	Matches []string `json:"matches"`
}
type joinedRouteRecordV1 struct {
	Missing     []string                     `json:"missing,omitempty"`
	Version     int                          `json:"version"`
	Bastions    int                          `json:"bastions"`
	Files       []joinedRouteFileRefV1       `json:"files"`
	Directories []joinedRouteDirectoryRefV1  `json:"directories"`
	Globs       []joinedRouteGlobRefV1       `json:"globs"`
	Credentials []joinedRouteCredentialRefV1 `json:"credentials"`
}

func (p *explicitRouteProjectionV1) record() (joinedRouteRecordV1, error) {
	record := joinedRouteRecordV1{Version: 1, Bastions: len(p.bastions), Missing: slices.Clone(p.source.readset.missing)}
	if err := p.check(context.Background()); err != nil {
		return record, err
	}
	for _, original := range p.source.readset.files {
		native, err := routeNativeIDV1(original.file)
		if err != nil {
			return record, err
		}
		record.Files = append(record.Files, joinedRouteFileRefV1{original.path, native, joinedDigest(original.data)})
	}
	for _, directory := range p.source.readset.directories {
		file, err := directory.root.Open(".")
		if err != nil {
			return record, err
		}
		native, err := routeNativeIDV1(file)
		err = errors.Join(err, file.Close())
		if err != nil {
			return record, err
		}
		record.Directories = append(record.Directories, joinedRouteDirectoryRefV1{directory.path, native})
	}
	for _, glob := range p.source.readset.globs {
		record.Globs = append(record.Globs, joinedRouteGlobRefV1{glob.pattern, slices.Clone(glob.matches)})
	}
	for _, original := range p.credentials {
		value := joinedRouteCredentialRefV1{Path: original.path, Missing: original.missing}
		if !original.missing {
			var native string
			var err error
			if original.socket {
				value.Socket = true
				native, err = routeSocketNativeIDV1(original.info)
			} else {
				native, err = routeNativeIDV1(original.file)
			}
			if err != nil {
				return record, err
			}
			value.Native = native
			value.Size = original.info.Size()
			value.Modified = original.info.ModTime()
		}
		record.Credentials = append(record.Credentials, value)
	}
	return record, nil
}

type joinedRouteRestrictionV1 struct {
	record      joinedRouteRecordV1
	readset     *explicitRouteReadsetV1
	credentials []*explicitRouteCredentialV1
	closed      bool
}

func openJoinedRouteRestrictionV1(ctx context.Context, record *joinedRouteRecordV1) (result *joinedRouteRestrictionV1, failure error) {
	if record == nil {
		return nil, nil
	}
	if record.Version != 1 || record.Bastions < 1 || record.Bastions > 8 || len(record.Files) < 1 || len(record.Files) > routeConfigCountLimitV1 || len(record.Directories) > 256 || len(record.Credentials) > 128 || len(record.Globs) > 256 || len(record.Missing) > 2 {
		return nil, routeSourceErrorV1("the original joined route restriction is unknown or oversized")
	}
	restriction := &joinedRouteRestrictionV1{record: *record, readset: &explicitRouteReadsetV1{missing: slices.Clone(record.Missing)}}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, restriction.close())
		}
	}()
	for _, expected := range record.Files {
		if !filepath.IsAbs(expected.Path) || strings.ContainsAny(expected.Path, "\x00\r\n") {
			return nil, routeSourceErrorV1("an original route source path is malformed")
		}
		info, err := os.Lstat(expected.Path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, routeSourceErrorV1("an original route source is linked or non-regular")
		}
		file, err := os.Open(expected.Path)
		if err != nil {
			return nil, err
		}
		opened, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, err
		}
		native, err := routeNativeIDV1(file)
		if err != nil || native != expected.Native || !os.SameFile(info, opened) {
			file.Close()
			return nil, routeSourceErrorV1("an original route source changed its native identity")
		}
		if err = routeConfigNativeSafeV1(file, opened); err != nil {
			file.Close()
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(file, routeConfigFileLimitV1+1))
		if err != nil || len(data) > routeConfigFileLimitV1 || joinedDigest(data) != expected.SHA256 {
			file.Close()
			return nil, routeSourceErrorV1("an original route source changed its bytes")
		}
		restriction.readset.files = append(restriction.readset.files, &routeConfigFileV1{expected.Path, file, opened, data})
	}
	for _, expected := range record.Directories {
		if !filepath.IsAbs(expected.Path) {
			return nil, routeSourceErrorV1("an original route directory path is malformed")
		}
		root, err := os.OpenRoot(expected.Path)
		if err != nil {
			return nil, err
		}
		file, err := root.Open(".")
		if err != nil {
			root.Close()
			return nil, err
		}
		info, err := file.Stat()
		native, idErr := routeNativeIDV1(file)
		err = errors.Join(err, idErr, file.Close())
		if err != nil || native != expected.Native {
			root.Close()
			return nil, routeSourceErrorV1("an original route source directory changed")
		}
		restriction.readset.directories = append(restriction.readset.directories, &routeConfigDirectoryV1{expected.Path, root, info})
	}
	for _, expected := range record.Globs {
		restriction.readset.globs = append(restriction.readset.globs, routeIncludeGlobV1{expected.Pattern, slices.Clone(expected.Matches)})
	}
	for _, expected := range record.Credentials {
		original := &explicitRouteCredentialV1{path: expected.Path, missing: expected.Missing}
		if !filepath.IsAbs(expected.Path) {
			return nil, routeSourceErrorV1("an original credential reference path is malformed")
		}
		if expected.Socket {
			if expected.Missing {
				return nil, routeSourceErrorV1("a stored socket cannot be an absent credential")
			}
			original, err := openRouteSocketV1(expected.Path, expected.Native)
			if err != nil {
				return nil, err
			}
			restriction.credentials = append(restriction.credentials, original)
			continue
		}
		if !expected.Missing {
			file, err := os.Open(expected.Path)
			if err != nil {
				return nil, err
			}
			info, err := file.Stat()
			native, idErr := routeNativeIDV1(file)
			if err != nil || idErr != nil || native != expected.Native || info.Size() != expected.Size || !info.ModTime().Equal(expected.Modified) {
				file.Close()
				return nil, routeSourceErrorV1("an original user-owned credential reference changed")
			}
			original.file, original.info = file, info
		}
		restriction.credentials = append(restriction.credentials, original)
	}
	if err := restriction.check(ctx); err != nil {
		return nil, err
	}
	keep = true
	return restriction, nil
}
func (s *joinedRouteRestrictionV1) check(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.closed {
		return routeSourceErrorV1("the original saved route restriction ended")
	}
	if err := s.readset.check(ctx); err != nil {
		return err
	}
	for _, original := range s.credentials {
		if original.socket {
			if err := original.checkSocket(); err != nil {
				return err
			}
			continue
		}
		current, err := os.Lstat(original.path)
		if original.missing {
			if !os.IsNotExist(err) {
				return routeSourceErrorV1("a previously absent original credential reference appeared")
			}
			continue
		}
		if err != nil {
			return err
		}
		opened, err := original.file.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(current, original.info) || !os.SameFile(opened, original.info) || opened.Size() != original.info.Size() || !opened.ModTime().Equal(original.info.ModTime()) {
			return routeSourceErrorV1("an original saved credential reference changed")
		}
		if err = routeConfigNativeSafeV1(original.file, opened); err != nil {
			return err
		}
	}
	return nil
}
func (s *joinedRouteRestrictionV1) close() error {
	if s == nil || s.closed {
		return nil
	}
	failure := s.readset.close()
	for _, credential := range s.credentials {
		if credential.parent != nil {
			failure = errors.Join(failure, credential.parent.Close())
		}
		if credential.file != nil {
			failure = errors.Join(failure, credential.file.Close())
		}
	}
	if failure == nil {
		s.closed = true
	}
	return failure
}
