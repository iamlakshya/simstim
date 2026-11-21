package supply

import (
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/BurntSushi/toml"
)

// MacRuntimePinV1 is a public source tuple, never runtime execution authority.
type MacRuntimePinV1 struct {
	Name     string `toml:"name"`
	Version  string `toml:"version"`
	Arch     string `toml:"arch"`
	URL      string `toml:"url"`
	SHA256   string `toml:"sha256"`
	Size     int64  `toml:"size"`
	Asset    string `toml:"asset"`
	Released string `toml:"released"`
}

// MacRuntimeLockV1 carries the two fixed public Mac runtime inputs.
type MacRuntimeLockV1 struct {
	Version int             `toml:"version"`
	Lima    MacRuntimePinV1 `toml:"lima"`
	Image   MacRuntimePinV1 `toml:"image"`
}

// ReadMacRuntimeLockV1 rejects unknown fields, names, locations and byte bounds.
func ReadMacRuntimeLockV1(reader io.Reader) (MacRuntimeLockV1, error) {
	var lock MacRuntimeLockV1
	data, err := io.ReadAll(io.LimitReader(reader, 64<<10+1))
	if err != nil {
		return lock, fmt.Errorf("read Mac runtime pins: %w", err)
	}
	if len(data) > 64<<10 {
		return lock, fmt.Errorf("the Mac runtime lock exceeds its bound")
	}
	metadata, err := toml.Decode(string(data), &lock)
	if err != nil {
		return lock, fmt.Errorf("parse Mac runtime pins: %w", err)
	}
	if len(metadata.Undecoded()) != 0 {
		return lock, fmt.Errorf("the Mac runtime lock has an unknown field")
	}
	if lock.Version != 1 || lock.Lima.Name != "lima" || Below(lock.Lima.Version, "2.3.0") || lock.Image.Name != "lima-ubuntu" {
		return lock, fmt.Errorf("the Mac runtime lock has unsupported fixed roles")
	}
	for _, pin := range []MacRuntimePinV1{lock.Lima, lock.Image} {
		parsed, err := url.Parse(pin.URL)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || pin.Arch != "arm64" || !IsHex64(pin.SHA256) || pin.Size <= 0 || pin.Size > 8<<30 || path.Base(pin.Asset) != pin.Asset || strings.ContainsAny(pin.Asset, "\x00\r\n\\") {
			return lock, fmt.Errorf("a pinned Mac input has invalid public metadata")
		}
	}
	if !strings.HasPrefix(lock.Lima.URL, "https://github.com/lima-vm/lima/releases/download/v"+lock.Lima.Version+"/") || lock.Lima.Asset != "lima-"+lock.Lima.Version+"-Darwin-arm64.tar.gz" {
		return lock, fmt.Errorf("the Lima pin is not its exact upstream release")
	}
	if !strings.HasPrefix(lock.Image.URL, "https://cloud-images.ubuntu.com/releases/") {
		return lock, fmt.Errorf("the Mac base image is not its pinned Ubuntu release")
	}
	return lock, nil
}

// MacRuntimeManifestItemsV1 preserves mirrored source byte identities while
// selecting this project's own release URLs. The producer checks actual files.
func MacRuntimeManifestItemsV1(lock MacRuntimeLockV1, release string) ([]Item, error) {
	items := []Item{}
	for _, entry := range []struct {
		kind string
		pin  MacRuntimePinV1
	}{{KindLima, lock.Lima}, {KindVMImage, lock.Image}} {
		pin := entry.pin
		item := Item{Kind: entry.kind, Name: pin.Name, Version: pin.Version, Arch: pin.Arch, SHA256: pin.SHA256, Size: pin.Size, URL: ReleaseFolder(release) + "/" + pin.Asset, Released: pin.Released}
		items = append(items, item)
	}
	manifest := Manifest{Release: release, Items: items}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return items, nil
}
