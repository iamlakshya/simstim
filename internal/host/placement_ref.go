package host

import (
	"encoding/json"
	"os"
	"time"
)

type purgeRef struct {
	Device  uint64    `json:"device"`
	Inode   uint64    `json:"inode"`
	UID     uint64    `json:"uid"`
	Mode    uint32    `json:"mode"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	extra   map[string]json.RawMessage
}

func refOf(info os.FileInfo) purgeRef {
	device, inode, uid, _, _ := footprintIdentity(info)
	return purgeRef{Device: device, Inode: inode, UID: uid, Mode: uint32(info.Mode()), Size: info.Size(), ModTime: info.ModTime()}
}

func (ref purgeRef) matches(info os.FileInfo) bool {
	if len(ref.extra) > 0 || info == nil {
		return false
	}
	device, inode, uid, links, ok := footprintIdentity(info)
	if !ok || ref.UID != uint64(os.Getuid()) || ref.Device != device || ref.Inode != inode || ref.UID != uid || ref.Mode != uint32(info.Mode()) {
		return false
	}
	if info.IsDir() {
		return info.Mode().Perm()&0o022 == 0
	}
	return (info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket != 0) && links == 1 && ref.Size == info.Size() && ref.ModTime.Equal(info.ModTime())
}

func (ref purgeRef) validDirectory() bool {
	return len(ref.extra) == 0 && ref.Inode != 0 && ref.UID == uint64(os.Getuid()) && os.FileMode(ref.Mode) == os.ModeDir|0o700
}

func (ref purgeRef) samePreparedIdentity(info os.FileInfo) bool {
	device, inode, uid, links, ok := footprintIdentity(info)
	return len(ref.extra) == 0 && ok && ref.UID == uint64(os.Getuid()) && ref.Device == device && ref.Inode == inode && ref.UID == uid && links == 1 && info.Mode().IsRegular() && (info.Mode().Perm() == 0o600 || info.Mode().Perm() == 0o644)
}

// UnmarshalJSON retains future native-ref fields and keeps them non-authoritative.
func (r *purgeRef) UnmarshalJSON(data []byte) error {
	type plain purgeRef
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"device", "inode", "uid", "mode", "size", "mtime"})
	*r = purgeRef(value)
	r.extra = extra
	return err
}

// MarshalJSON preserves future native-ref fields across journal rewrites.
func (r purgeRef) MarshalJSON() ([]byte, error) {
	type plain purgeRef
	return encodeFootprint(plain(r), r.extra)
}

func (ref purgeRef) matchesRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !ref.matches(info) {
		return placementError("the anchored directory changed its native generation")
	}
	return nil
}

func samePurgeDirectoryRef(a, b purgeRef) bool {
	return len(a.extra) == 0 && len(b.extra) == 0 && a.Inode != 0 && a.UID == uint64(os.Getuid()) && a.Device == b.Device && a.Inode == b.Inode && a.UID == b.UID && a.Mode == b.Mode && os.FileMode(a.Mode).IsDir()
}
