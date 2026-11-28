// Package supply loads and validates guest/supply.lock, builds and checks
// release-manifest.json, and appends or verifies raw dm-verity trees for the
// microvm boot pair (isolation spec 14.9.1 and 20.1).
package supply

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tomlv2 "github.com/pelletier/go-toml/v2"
)

// Lock is the parsed contents of guest/supply.lock (isolation spec 20.1).
type Lock struct {
	Version int        `toml:"version"`
	Pins    []Pin      `toml:"pin"`
	Kernel  KernelSpec `toml:"kernel"`
	VMMs    []VMMSpec  `toml:"vmm"`
}

// Pin is one [[pin]] entry watched by scripts/pins-watch.py (spec 19.5).
type Pin struct {
	Name   string `toml:"name"`
	Repo   string `toml:"repo"`
	Ref    string `toml:"ref"`
	Commit string `toml:"commit"`
	Watch  string `toml:"watch"`
	Tag    string `toml:"tag"`
}

// AssetPin names one downloadable upstream file verified by SHA-256 and size.
type AssetPin struct {
	URL    string `toml:"url"`
	SHA256 string `toml:"sha256"`
	Size   int64  `toml:"size"`
}

// KernelArch pins one architecture's prebuilt Kata substrate archive, extracted
// kernel image and kernel config (spec 20.1, decision 0053).
type KernelArch struct {
	URL           string `toml:"url"`
	ArchiveSHA256 string `toml:"archive_sha256"`
	ArchiveSize   int64  `toml:"archive_size"`
	SHA256        string `toml:"sha256"`
	Size          int64  `toml:"size"`
	ConfigSHA256  string `toml:"config_sha256"`
	ConfigSize    int64  `toml:"config_size"`
}

// KernelSpec is the [kernel] table of guest/supply.lock.
type KernelSpec struct {
	Source        string     `toml:"source"`
	KataVersion   string     `toml:"kata_version"`
	Version       string     `toml:"version"`
	Licence       string     `toml:"licence"`
	ArchiveMember string     `toml:"archive_member"`
	ConfigMember  string     `toml:"config_member"`
	SourceTarball AssetPin   `toml:"source_tarball"`
	AMD64         KernelArch `toml:"amd64"`
	ARM64         KernelArch `toml:"arm64"`
}

// Arch returns the KernelArch pin for arch ("amd64" or "arm64").
func (k KernelSpec) Arch(arch string) (KernelArch, error) {
	switch arch {
	case "amd64":
		return k.AMD64, nil
	case "arm64":
		return k.ARM64, nil
	default:
		return KernelArch{}, fmt.Errorf("supply: unknown kernel architecture %q. Try: use amd64 or arm64", arch)
	}
}

// VMMSpec is one [[vmm]] table of guest/supply.lock.
type VMMSpec struct {
	Name          string   `toml:"name"`
	Version       string   `toml:"version"`
	AdvisoryFloor string   `toml:"advisory_floor"`
	Licence       string   `toml:"licence"`
	HostKernel    string   `toml:"host_kernel"`
	Released      any      `toml:"released"`
	EOL           any      `toml:"eol"`
	AMD64         AssetPin `toml:"amd64"`
	ARM64         AssetPin `toml:"arm64"`
	RemoteAMD64   AssetPin `toml:"remote_amd64"`
	RemoteARM64   AssetPin `toml:"remote_arm64"`
	Source        AssetPin `toml:"source"`
}

// EOLString formats the VMM's EOL date as YYYY-MM-DD when set.
func (v VMMSpec) EOLString() string { return formatTOMLDate(v.EOL) }

// ReleasedString formats the VMM's release date as YYYY-MM-DD when set.
func (v VMMSpec) ReleasedString() string { return formatTOMLDate(v.Released) }

func formatTOMLDate(v any) string {
	switch x := v.(type) {
	case tomlv2.LocalDate:
		return x.String()
	case time.Time:
		return x.UTC().Format("2006-01-02")
	case string:
		return strings.TrimSpace(x)
	default:
		return ""
	}
}

// BinaryFor returns the VMM binary and ch-remote pins for arch.
func (v VMMSpec) BinaryFor(arch string) (vmm, remote AssetPin, err error) {
	switch arch {
	case "amd64":
		return v.AMD64, v.RemoteAMD64, nil
	case "arm64":
		return v.ARM64, v.RemoteARM64, nil
	default:
		return AssetPin{}, AssetPin{}, fmt.Errorf("supply: unknown VMM architecture %q. Try: use amd64 or arm64", arch)
	}
}

// FindVMM returns the [[vmm]] entry named name from the lock.
func (l Lock) FindVMM(name string) (VMMSpec, error) {
	for _, v := range l.VMMs {
		if v.Name == name {
			return v, nil
		}
	}
	return VMMSpec{}, fmt.Errorf("supply: no [[vmm]] named %q in guest/supply.lock. Try: check [[vmm]] entries in guest/supply.lock", name)
}

// LoadLock reads and validates guest/supply.lock from path.
func LoadLock(path string) (Lock, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Lock{}, fmt.Errorf("supply: read %s: %w. Try: pass --lock guest/supply.lock from the repository root", path, err)
	}
	return ParseLock(raw)
}

// ParseLock decodes TOML bytes into Lock using strict field decoding and checks
// every pin and cross-table version rule (spec 20.1).
func ParseLock(data []byte) (Lock, error) {
	var l Lock
	dec := tomlv2.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return Lock{}, fmt.Errorf("supply: parse guest/supply.lock: %w. Try: fix the TOML syntax or field name in guest/supply.lock", err)
	}
	if err := CheckLock(l); err != nil {
		return Lock{}, err
	}
	return l, nil
}

// CheckLock validates guest/supply.lock invariants including spec 20.1's rule
// that every named table matches its [[pin]] entry after stripping leading
// non-digits from ref.
func CheckLock(l Lock) error {
	if l.Version != 1 {
		return fmt.Errorf("supply: guest/supply.lock version is %d, want 1. Try: set version = 1 in guest/supply.lock", l.Version)
	}
	pinsByName := make(map[string]Pin, len(l.Pins))
	for _, p := range l.Pins {
		if p.Name == "" || p.Ref == "" || len(p.Commit) != 40 || !isLowerHex(p.Commit) {
			return fmt.Errorf("supply: invalid [[pin]] entry %q in guest/supply.lock. Try: set name, ref and 40-char lowercase commit", p.Name)
		}
		if _, dup := pinsByName[p.Name]; dup {
			return fmt.Errorf("supply: duplicate [[pin]] name %q in guest/supply.lock. Try: give each pin a unique name", p.Name)
		}
		pinsByName[p.Name] = p
	}
	if err := checkKernelSpec(l.Kernel, pinsByName); err != nil {
		return err
	}
	if len(l.VMMs) == 0 {
		return errors.New("supply: guest/supply.lock has no [[vmm]] entry. Try: pin cloud-hypervisor in guest/supply.lock")
	}
	for _, v := range l.VMMs {
		if err := checkVMMSpec(v, pinsByName); err != nil {
			return err
		}
	}
	return nil
}

func checkKernelSpec(k KernelSpec, pins map[string]Pin) error {
	if k.Version == "" || k.KataVersion == "" {
		return errors.New("supply: [kernel] must set version and kata_version in guest/supply.lock. Try: check [kernel] in guest/supply.lock")
	}
	if k.ArchiveMember == "" || k.ConfigMember == "" {
		return errors.New("supply: [kernel] must set archive_member and config_member. Try: check [kernel] in guest/supply.lock")
	}
	pin, ok := pins["kata-kernel"]
	if !ok {
		return errors.New("supply: [kernel] has no matching [[pin]] named \"kata-kernel\". Try: add [[pin]] name = \"kata-kernel\" to guest/supply.lock")
	}
	if stripLeadingNonDigits(pin.Ref) != stripLeadingNonDigits(k.KataVersion) {
		return fmt.Errorf("supply: [kernel].kata_version (%s) disagrees with [[pin]] kata-kernel ref (%s). Try: update both together in guest/supply.lock", k.KataVersion, pin.Ref)
	}
	if err := checkAssetPin("kernel.source_tarball", k.SourceTarball); err != nil {
		return err
	}
	for arch, ka := range map[string]KernelArch{"amd64": k.AMD64, "arm64": k.ARM64} {
		if err := checkHTTPSURL("kernel."+arch, ka.URL); err != nil {
			return err
		}
		if !isSHA256Hex(ka.ArchiveSHA256) || ka.ArchiveSize <= 0 {
			return fmt.Errorf("supply: kernel.%s needs valid archive_sha256 and archive_size > 0. Try: update guest/supply.lock", arch)
		}
		if !isSHA256Hex(ka.SHA256) || ka.Size <= 0 {
			return fmt.Errorf("supply: kernel.%s needs valid sha256 and size > 0. Try: update guest/supply.lock", arch)
		}
		if !isSHA256Hex(ka.ConfigSHA256) || ka.ConfigSize <= 0 {
			return fmt.Errorf("supply: kernel.%s needs valid config_sha256 and config_size > 0. Try: update guest/supply.lock", arch)
		}
	}
	return nil
}

func checkVMMSpec(v VMMSpec, pins map[string]Pin) error {
	if v.Name == "" || v.Version == "" {
		return errors.New("supply: [[vmm]] entry is missing name or version. Try: check [[vmm]] in guest/supply.lock")
	}
	pin, ok := pins[v.Name]
	if !ok {
		return fmt.Errorf("supply: [[vmm]] %q has no matching [[pin]] entry. Try: add [[pin]] name = %q to guest/supply.lock", v.Name, v.Name)
	}
	if stripLeadingNonDigits(pin.Ref) != stripLeadingNonDigits(v.Version) {
		return fmt.Errorf("supply: [[vmm]] %q version (%s) disagrees with [[pin]] ref (%s). Try: update both together in guest/supply.lock", v.Name, v.Version, pin.Ref)
	}
	for label, a := range map[string]AssetPin{
		v.Name + ".amd64":        v.AMD64,
		v.Name + ".arm64":        v.ARM64,
		v.Name + ".remote_amd64": v.RemoteAMD64,
		v.Name + ".remote_arm64": v.RemoteARM64,
		v.Name + ".source":       v.Source,
	} {
		if err := checkAssetPin(label, a); err != nil {
			return err
		}
	}
	return nil
}

func checkAssetPin(label string, a AssetPin) error {
	if err := checkHTTPSURL(label, a.URL); err != nil {
		return err
	}
	if !isSHA256Hex(a.SHA256) {
		return fmt.Errorf("supply: %s has invalid sha256 %q. Try: use a 64-character lowercase SHA-256 hex digest", label, a.SHA256)
	}
	if a.Size <= 0 {
		return fmt.Errorf("supply: %s size must be positive. Try: set the exact byte size in guest/supply.lock", label)
	}
	return nil
}

func checkHTTPSURL(label, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return fmt.Errorf("supply: %s URL %q must be an HTTPS URL. Try: use an https:// release URL in guest/supply.lock", label, raw)
	}
	return nil
}

// stripLeadingNonDigits drops any prefix before the first ASCII digit, matching
// spec 20.1 / 20.9 (for example "v55.0" -> "55.0", "release-20261123.1" -> "20261123.1").
func stripLeadingNonDigits(s string) string {
	for i := range len(s) {
		if s[i] >= '0' && s[i] <= '9' {
			return s[i:]
		}
	}
	return s
}

func isSHA256Hex(s string) bool { return len(s) == 64 && isLowerHex(s) }

func isLowerHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}
