package image

import (
	"archive/tar"
	"encoding/binary"
	"hash"
	"io/fs"
	"time"
)

type agentsIdentity struct {
	device, inode        uint64
	seconds, nanoseconds int64
}
type agentsEntry struct {
	name       string
	header     *tar.Header
	identity   agentsIdentity
	sourceSize int64
	sourceMode fs.FileMode
}

func agentsHeader(name string, info fs.FileInfo, link string) (agentsEntry, error) {
	if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return agentsEntry{}, agentsDiskError("an install folder contains a special file")
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return agentsEntry{}, agentsDiskError("installed file metadata cannot be represented in tar")
	}
	if header.Size < 0 {
		return agentsEntry{}, agentsDiskError("an installed file has a negative size")
	}
	header.Name = name
	header.Uid, header.Gid = 1000, 1000
	header.Uname, header.Gname = "", ""
	header.Format = tar.FormatPAX
	header.AccessTime = time.Time{}
	header.ChangeTime = time.Time{}
	header.PAXRecords = nil
	header.Xattrs = nil
	identity, err := agentsFileIdentity(info)
	if err != nil {
		return agentsEntry{}, err
	}
	return agentsEntry{name: name, header: header, identity: identity, sourceSize: info.Size(), sourceMode: info.Mode()}, nil
}

func agentsSynthetic(name string, kind byte, link string) agentsEntry {
	mode := int64(0o755)
	if kind == tar.TypeSymlink {
		mode = 0o777
	}
	return agentsEntry{name: name, header: &tar.Header{Name: name, Typeflag: kind, Mode: mode, Uid: 1000, Gid: 1000, Linkname: link, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX}}
}

func hashAgentsEntry(digest hash.Hash, entry agentsEntry) {
	// Fixed fields and length-prefixed exact bytes make names unambiguous.
	// Device complements the specified inode field for cross-filesystem swaps.
	for _, text := range []string{entry.name, entry.header.Linkname} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(text)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write([]byte(text))
	}
	fields := []uint64{uint64(entry.header.Typeflag), uint64(entry.sourceSize), uint64(entry.header.Mode), uint64(entry.header.ModTime.Unix()), uint64(entry.header.ModTime.Nanosecond()), uint64(entry.identity.seconds), uint64(entry.identity.nanoseconds), entry.identity.inode, entry.identity.device}
	var encoded [8]byte
	for _, value := range fields {
		binary.BigEndian.PutUint64(encoded[:], value)
		_, _ = digest.Write(encoded[:])
	}
}
