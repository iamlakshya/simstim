package bundle

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A small reader of profiles, in place of a new dependency on the pprof
// project's parser. A profile is a gzipped protocol buffer, a Profile
// message ([profile.proto]). Its strings are all in one table, and other
// fields name them by their index. The bundle rewrites only two kinds of
// strings: the file name of each mapping, and the file name of each
// function. Function names stay as they are.
//
// The reader walks the fields three times, and keeps none of them: a
// profile comes from a host, and a small file could otherwise make the
// client hold far more memory than the file's size.
//
// [profile.proto]: https://github.com/google/pprof/blob/main/proto/profile.proto

// The fields of profile.proto that the reader needs.
const (
	profMapping     = 3 // Profile.mapping, a Mapping message
	profFunction    = 5 // Profile.function, a Function message
	profStringTable = 6 // Profile.string_table
	mappingFilename = 5 // Mapping.filename, an index into the table
	functionFile    = 4 // Function.filename, an index into the table
)

// The wire types of protocol buffers.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// The limits of a profile that the bundle reads: its size once gunzipped,
// its fields, and its strings. A profile from the debug mode is a few MiB
// at most.
const (
	maxProfile = 16 << 20
	maxFields  = 1 << 20
	maxStrings = 1 << 18
)

// errProfileLimit is the error of a profile beyond a limit.
var errProfileLimit = errors.New("the profile is larger than the bundle reads")

// pbField is one field of a protocol buffer message.
type pbField struct {
	num  int
	wire int
	raw  []byte // the whole field, with its tag
	val  []byte // the bytes of a length-delimited field
	v    uint64 // the value of a varint
}

// walkFields calls fn for each field of a message, in order. It keeps no
// field, and stops at maxFields.
func walkFields(b []byte, fn func(pbField) error) error {
	count := 0
	for i := 0; i < len(b); {
		f, n, err := readField(b[i:])
		if err != nil {
			return err
		}
		if count++; count > maxFields {
			return errProfileLimit
		}
		f.raw = b[i : i+n]
		if err := fn(f); err != nil {
			return err
		}
		i += n
	}
	return nil
}

// readField reads the field at the start of b, and returns it and its
// length.
func readField(b []byte) (pbField, int, error) {
	tag, i := binary.Uvarint(b)
	if i <= 0 || tag>>3 == 0 {
		return pbField{}, 0, errors.New("a bad tag")
	}
	f := pbField{num: int(tag >> 3), wire: int(tag & 7)}
	switch f.wire {
	case wireVarint:
		v, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return pbField{}, 0, errors.New("a bad varint")
		}
		f.v, i = v, i+n
	case wireFixed64:
		i += 8
	case wireBytes:
		l, n := binary.Uvarint(b[i:])
		if n <= 0 || l > uint64(len(b)-i-n) { //nolint:gosec // G115: i+n is at most len(b), so the difference is not negative
			return pbField{}, 0, errors.New("a bad length")
		}
		i += n
		end := i + int(l) //nolint:gosec // G115: l is at most what is left of b, checked above
		f.val, i = b[i:end], end
	case wireFixed32:
		i += 4
	default:
		return pbField{}, 0, fmt.Errorf("the wire type %d", f.wire)
	}
	if i > len(b) {
		return pbField{}, 0, errors.New("a field past the end")
	}
	return f, i, nil
}

// profString is one string of a profile's table, and whether it is a file
// name.
type profString struct {
	s    string
	file bool
}

// rewriteProfile gunzips a profile, rewrites the file name of each mapping
// and each function with rewrite, and gzips it again. It also returns the
// table of strings after the rewrite, for the last check.
func rewriteProfile(data []byte, rewrite func(string) string) ([]byte, []profString, error) {
	plain, err := gunzipProfile(data)
	if err != nil {
		return nil, nil, err
	}
	var table []profString
	err = walkFields(plain, func(f pbField) error {
		if f.num != profStringTable || f.wire != wireBytes {
			return nil
		}
		if len(table) == maxStrings {
			return errProfileLimit
		}
		table = append(table, profString{s: string(f.val)})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read the profile: %w", err)
	}
	err = walkFields(plain, func(f pbField) error {
		switch {
		case f.num == profMapping && f.wire == wireBytes:
			return markFile(f.val, mappingFilename, table)
		case f.num == profFunction && f.wire == wireBytes:
			return markFile(f.val, functionFile, table)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read the profile: %w", err)
	}
	for i := range table {
		if table[i].file {
			table[i].s = rewrite(table[i].s)
		}
	}
	out, err := writeProfile(plain, table)
	if err != nil {
		return nil, nil, err
	}
	return out, table, nil
}

// gunzipProfile gunzips a profile, up to maxProfile.
func gunzipProfile(data []byte) ([]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gunzip the profile: %w", err)
	}
	plain, err := io.ReadAll(io.LimitReader(z, maxProfile+1))
	if err != nil {
		return nil, fmt.Errorf("gunzip the profile: %w", err)
	}
	if len(plain) > maxProfile {
		return nil, errProfileLimit
	}
	return plain, nil
}

// writeProfile writes the profile again, with the strings of table in place
// of its own, and gzips it.
func writeProfile(plain []byte, table []profString) ([]byte, error) {
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	next := 0
	err := walkFields(plain, func(f pbField) error {
		if f.num != profStringTable || f.wire != wireBytes {
			_, err := zw.Write(f.raw)
			return err //nolint:wrapcheck // a write to a buffer
		}
		s := table[next].s
		next++
		_, _ = zw.Write(binary.AppendUvarint(nil, profStringTable<<3|wireBytes))
		_, _ = zw.Write(binary.AppendUvarint(nil, uint64(len(s))))
		_, err := zw.Write([]byte(s))
		return err //nolint:wrapcheck // a write to a buffer
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("write the profile: %w", err)
	}
	return out.Bytes(), nil
}

// markFile marks the string that field num of a message names, when the
// message has it.
func markFile(msg []byte, num int, table []profString) error {
	return walkFields(msg, func(f pbField) error {
		if f.num == num && f.wire == wireVarint && f.v < uint64(len(table)) {
			table[f.v].file = true
		}
		return nil
	})
}
