package supply

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var (
	hex40       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pinNameForm = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	repoForm    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	refForm     = regexp.MustCompile(`^[!-~]{1,200}$`)
	tagForm     = regexp.MustCompile(`^[A-Za-z0-9._/-]*\{version\}[A-Za-z0-9._/-]*$`)
)

const maxLockBytes = 1 << 20

// Lock is the parsed content of guest/supply.lock (isolation spec 19.5 and 20.1).
type Lock struct {
	Version         int         `toml:"version"`
	Pins            []LockPin   `toml:"pin"`
	Kernel          LockKernel  `toml:"kernel"`
	ErofsUtils      LockArchive `toml:"erofs_utils"`
	MobyCheckConfig LockMoby    `toml:"moby_check_config"`
	Builder         LockBuilder `toml:"builder"`
	VMMs            []LockVMM   `toml:"vmm"`
}

// LockPin is one [[pin]] entry watched by scripts/pins-watch.py (spec 19.5).
type LockPin struct {
	Name   string `toml:"name"`
	Repo   string `toml:"repo"`
	Ref    string `toml:"ref"`
	Commit string `toml:"commit"`
	Watch  string `toml:"watch"`
	Tag    string `toml:"tag"`
}

// LockKernel is the [kernel] table of guest/supply.lock (spec 20.1).
type LockKernel struct {
	Version  string    `toml:"version"`
	Released time.Time `toml:"released"`
	URL      string    `toml:"url"`
	SHA256   string    `toml:"sha256"`
}

// ReleasedDate returns the kernel's upstream release date as YYYY-MM-DD.
func (k LockKernel) ReleasedDate() string {
	if k.Released.IsZero() {
		return ""
	}
	return k.Released.Format(time.DateOnly)
}

// LockArchive is an archive table pinned by version, URL and SHA-256.
type LockArchive struct {
	Version string `toml:"version"`
	URL     string `toml:"url"`
	SHA256  string `toml:"sha256"`
}

// LockMoby pins moby's contrib/check-config.sh by commit and file SHA-256.
type LockMoby struct {
	Commit string `toml:"commit"`
	SHA256 string `toml:"sha256"`
}

// LockBuilder pins the container and the signed Debian package snapshot used
// for reproducible builds. The InRelease digests bind the package checksums:
// one for the suite, and one for its security suite at the same snapshot time,
// whose updates the pinned container already carries.
type LockBuilder struct {
	Image                   string `toml:"image"`
	Snapshot                string `toml:"snapshot"`
	Suite                   string `toml:"suite"`
	InReleaseSHA256         string `toml:"inrelease_sha256"`
	SecurityInReleaseSHA256 string `toml:"security_inrelease_sha256"`
}

// LockFile pins a downloaded binary or source archive by URL, SHA-256 and optional size.
type LockFile struct {
	URL    string `toml:"url"`
	SHA256 string `toml:"sha256"`
	Size   int64  `toml:"size"`
}

// LockVMM is one [[vmm]] table of guest/supply.lock (spec 20.1 and 20.3).
type LockVMM struct {
	Name     string    `toml:"name"`
	Version  string    `toml:"version"`
	Released time.Time `toml:"released"`
	EOL      time.Time `toml:"eol"`
	AMD64    LockFile  `toml:"amd64"`
	ARM64    LockFile  `toml:"arm64"`
	Source   LockFile  `toml:"source"`
}

// ReleasedDate returns the VMM's upstream release date as YYYY-MM-DD.
func (v LockVMM) ReleasedDate() string {
	if v.Released.IsZero() {
		return ""
	}
	return v.Released.Format(time.DateOnly)
}

// EOLDate returns the VMM's end-of-fixes date as YYYY-MM-DD.
func (v LockVMM) EOLDate() string {
	if v.EOL.IsZero() {
		return ""
	}
	return v.EOL.Format(time.DateOnly)
}

// LoadLockFile reads and validates guest/supply.lock from disk.
func LoadLockFile(p string) (*Lock, error) {
	f, err := openRegular(p)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxLockBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if len(b) > maxLockBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", p, maxLockBytes)
	}
	return LoadLock(b)
}

// LoadLock parses and validates guest/supply.lock TOML bytes. It refuses
// unknown keys and enforces agreement between named tables and [[pin]] entries.
func LoadLock(data []byte) (*Lock, error) {
	var l Lock
	md, err := toml.Decode(string(data), &l)
	if err != nil {
		return nil, fmt.Errorf("parse guest/supply.lock: %w", err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return nil, fmt.Errorf("guest/supply.lock has unknown key %q", extra[0].String())
	}
	if err := checkEncodedTagForms(data); err != nil {
		return nil, err
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

// The optional tag string has a default when omitted. Keep an explicit
// empty value distinct from omission, as the pins watch does.
func checkEncodedTagForms(data []byte) error {
	var fields struct {
		Pins []struct {
			Name string  `toml:"name"`
			Tag  *string `toml:"tag"`
		} `toml:"pin"`
	}
	if _, err := toml.Decode(string(data), &fields); err != nil {
		return fmt.Errorf("read pin tag forms: %w", err)
	}
	for _, pin := range fields.Pins {
		if pin.Tag != nil && !tagForm.MatchString(*pin.Tag) {
			return fmt.Errorf("guest/supply.lock: pin %q has invalid tag form %q", pin.Name, *pin.Tag)
		}
	}
	return nil
}

// Validate checks every table of guest/supply.lock and ensures that [kernel],
// [erofs_utils] and each [[vmm]] agree with their [[pin]] entry (spec 20.1).
func (l *Lock) Validate() error {
	if l.Version != 1 {
		return fmt.Errorf("guest/supply.lock: version %d is not 1", l.Version)
	}
	pins, err := validateLockPins(l.Pins)
	if err != nil {
		return err
	}
	if err := l.validateKernel(pins); err != nil {
		return err
	}
	if err := l.validateErofs(pins); err != nil {
		return err
	}
	if !hex40.MatchString(l.MobyCheckConfig.Commit) {
		return fmt.Errorf("guest/supply.lock: [moby_check_config] commit %q is not 40 lower-case hex digits", l.MobyCheckConfig.Commit)
	}
	if !hex64.MatchString(l.MobyCheckConfig.SHA256) {
		return fmt.Errorf("guest/supply.lock: [moby_check_config] sha256 %q is not 64 lower-case hex digits", l.MobyCheckConfig.SHA256)
	}
	if err := validateBuilder(l.Builder); err != nil {
		return err
	}
	if len(l.VMMs) == 0 {
		return errors.New("guest/supply.lock: needs at least one [[vmm]] table")
	}
	seenVMM := map[string]bool{}
	for _, v := range l.VMMs {
		if seenVMM[v.Name] {
			return fmt.Errorf("guest/supply.lock: [[vmm]] %q appears twice", v.Name)
		}
		seenVMM[v.Name] = true
		if err := validateVMM(v, pins); err != nil {
			return err
		}
	}
	return nil
}

func validateLockPins(list []LockPin) (map[string]LockPin, error) {
	pins := make(map[string]LockPin, len(list))
	for i, p := range list {
		switch {
		case !pinNameForm.MatchString(p.Name):
			return nil, fmt.Errorf("guest/supply.lock: pin %d name %q is invalid", i+1, p.Name)
		case pins[p.Name].Name != "":
			return nil, fmt.Errorf("guest/supply.lock: pin %q appears twice", p.Name)
		case !refForm.MatchString(p.Ref):
			return nil, fmt.Errorf("guest/supply.lock: pin %q has invalid ref %q", p.Name, p.Ref)
		case p.Tag != "" && !tagForm.MatchString(p.Tag):
			return nil, fmt.Errorf("guest/supply.lock: pin %q has invalid tag form %q", p.Name, p.Tag)
		case !hex40.MatchString(p.Commit):
			return nil, fmt.Errorf("guest/supply.lock: pin %q commit %q is not 40 lower-case hex digits", p.Name, p.Commit)
		}
		watch := p.Watch
		if watch == "" {
			watch = "github"
		}
		if watch != "github" && watch != "osv" {
			return nil, fmt.Errorf("guest/supply.lock: pin %q watch %q must be github or osv", p.Name, p.Watch)
		}
		if watch == "github" && !repoForm.MatchString(p.Repo) {
			return nil, fmt.Errorf("guest/supply.lock: pin %q needs repo as owner/name", p.Name)
		}
		pins[p.Name] = p
	}
	return pins, nil
}

func (l *Lock) validateKernel(pins map[string]LockPin) error {
	k := l.Kernel
	switch {
	case !cleanVersion(k.Version):
		return fmt.Errorf("guest/supply.lock: [kernel] version %q is not a clean release version", k.Version)
	case !strings.HasPrefix(k.Version, "6.18."):
		return fmt.Errorf("guest/supply.lock: [kernel] version %q is outside the 6.18 long-term line", k.Version)
	case !isDate(k.ReleasedDate()):
		return errors.New("guest/supply.lock: [kernel] needs released as YYYY-MM-DD")
	case !hex64.MatchString(k.SHA256):
		return fmt.Errorf("guest/supply.lock: [kernel] sha256 %q is not 64 lower-case hex digits", k.SHA256)
	}
	if err := checkURL(k.URL); err != nil {
		return fmt.Errorf("guest/supply.lock: [kernel] url: %w", err)
	}
	wantSuffix := "/" + KernelSourceAsset(k.Version)
	if !strings.HasSuffix(k.URL, wantSuffix) {
		return fmt.Errorf("guest/supply.lock: [kernel] url %q does not end with %s", k.URL, wantSuffix)
	}
	if err := checkPinAgreement("[kernel]", "kernel", k.Version, pins); err != nil {
		return err
	}
	return checkLockAdvisories(KindKernel, "guest", k.Version)
}

func (l *Lock) validateErofs(pins map[string]LockPin) error {
	e := l.ErofsUtils
	switch {
	case !cleanVersion(e.Version):
		return fmt.Errorf("guest/supply.lock: [erofs_utils] version %q is not a clean release version", e.Version)
	case Below(e.Version, "1.9.3"):
		return fmt.Errorf("guest/supply.lock: [erofs_utils] version %s is below 1.9.3", e.Version)
	case !hex64.MatchString(e.SHA256):
		return fmt.Errorf("guest/supply.lock: [erofs_utils] sha256 %q is not 64 lower-case hex digits", e.SHA256)
	}
	if err := checkURL(e.URL); err != nil {
		return fmt.Errorf("guest/supply.lock: [erofs_utils] url: %w", err)
	}
	return checkPinAgreement("[erofs_utils]", "erofs-utils", e.Version, pins)
}

func validateVMM(v LockVMM, pins map[string]LockPin) error {
	switch {
	case !pinNameForm.MatchString(v.Name):
		return fmt.Errorf("guest/supply.lock: [[vmm]] name %q is invalid", v.Name)
	case !cleanVersion(v.Version):
		return fmt.Errorf("guest/supply.lock: [[vmm]] %s version %q is not a clean release version", v.Name, v.Version)
	case v.Name == FirecrackerName && Below(v.Version, "1.18.0"):
		return fmt.Errorf("guest/supply.lock: Firecracker release pin %s is below 1.18.0", v.Version)
	case !isDate(v.ReleasedDate()):
		return fmt.Errorf("guest/supply.lock: [[vmm]] %s needs released as YYYY-MM-DD", v.Name)
	case !isDate(v.EOLDate()):
		return fmt.Errorf("guest/supply.lock: [[vmm]] %s needs eol as YYYY-MM-DD", v.Name)
	case v.EOL.Before(v.Released):
		return fmt.Errorf("guest/supply.lock: [[vmm]] %s fixes end before its release", v.Name)
	}
	for arch, f := range map[string]LockFile{"amd64": v.AMD64, "arm64": v.ARM64} {
		if err := checkURL(f.URL); err != nil {
			return fmt.Errorf("guest/supply.lock: [[vmm]] %s %s url: %w", v.Name, arch, err)
		}
		if !hex64.MatchString(f.SHA256) {
			return fmt.Errorf("guest/supply.lock: [[vmm]] %s %s sha256 %q is not 64 lower-case hex digits", v.Name, arch, f.SHA256)
		}
		if f.Size <= 0 {
			return fmt.Errorf("guest/supply.lock: [[vmm]] %s %s needs size > 0", v.Name, arch)
		}
	}
	if err := checkURL(v.Source.URL); err != nil {
		return fmt.Errorf("guest/supply.lock: [[vmm]] %s source url: %w", v.Name, err)
	}
	if !hex64.MatchString(v.Source.SHA256) {
		return fmt.Errorf("guest/supply.lock: [[vmm]] %s source sha256 %q is not 64 lower-case hex digits", v.Name, v.Source.SHA256)
	}
	if v.Source.Size <= 0 {
		return fmt.Errorf("guest/supply.lock: [[vmm]] %s source needs size > 0", v.Name)
	}
	if err := checkPinAgreement("[[vmm]] "+v.Name, v.Name, v.Version, pins); err != nil {
		return err
	}
	return checkLockAdvisories(KindVMM, v.Name, v.Version)
}

func checkLockAdvisories(kind, name, version string) error {
	for _, advisory := range KnownBad() {
		if advisory.Matches(kind, name, version) {
			return fmt.Errorf("guest/supply.lock: %s %s at %s matches advisory %s", kind, name, version, advisory.ID)
		}
	}
	return nil
}

// checkPinAgreement enforces spec 20.1: each named table with advisories has a
// [[pin]] entry whose ref, after dropping text before the first digit, equals
// the table's version.
func checkPinAgreement(table, pinName, wantVersion string, pins map[string]LockPin) error {
	p, ok := pins[pinName]
	if !ok {
		return fmt.Errorf("guest/supply.lock: %s has no [[pin]] named %q", table, pinName)
	}
	got := pinRefVersion(p.Ref)
	if got != wantVersion {
		return fmt.Errorf("guest/supply.lock: %s says version %q, and [[pin]] %q says ref %q (%q)", table, wantVersion, pinName, p.Ref, got)
	}
	return nil
}

// pinRefVersion drops every character before the first ASCII digit of ref,
// turning tags like "v54.1" or "release-20261102.0" into "54.1" or "20261102.0".
func pinRefVersion(ref string) string {
	for i := range len(ref) {
		if ref[i] >= '0' && ref[i] <= '9' {
			return ref[i:]
		}
	}
	return ""
}

// CheckPinnedSHA256 checks the SHA-256 of path against wantHex, returning an
// error on mismatch.
func CheckPinnedSHA256(path, wantHex string) error {
	if !hex64.MatchString(wantHex) {
		return fmt.Errorf("invalid sha256 %q", wantHex)
	}
	f, err := openRegular(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHex {
		return &MismatchError{Name: filepath.Base(path), What: "SHA-256", Want: wantHex, Got: got}
	}
	return nil
}
