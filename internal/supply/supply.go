// Package supply holds the release manifest: the SHA-256 of each trusted
// artefact of a release, for each architecture (isolation spec 14.9.1). The
// client embeds it, so the laptop holds the digests and the hosts hold the
// bytes. A host never decides what is trusted.
//
// The manifest has no key, minimum version or expiry of its own: the release
// that carries the client is signed (decision 0017), and every check uses the
// client's own digests, so a host cannot roll back.
package supply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// FileName is the release manifest's file: next to the helpers that the
// client embeds, in the helper bundle, and on the release.
const FileName = "release-manifest.json"

// The kinds of item (isolation spec 14.9.1). Later tasks add the items of
// the kinds after vt, and the format accepts them all now.
const (
	KindHelper  = "helper"   // the host helper
	KindVT      = "vt"       // the shadow terminal
	KindUserns  = "userns"   // the namespace launcher of the process wall
	KindVMM     = "vmm"      // a virtual machine monitor, such as Cloud Hypervisor
	KindKernel  = "kernel"   // the guest kernel
	KindTools   = "tools"    // the tools image that a microvm boots from
	KindImage   = "image"    // the default image, pinned by digest
	KindGvisor  = "gvisor"   // runsc
	KindLaunch  = "launch"   // the slot launcher of separate accounts
	KindLima    = "lima"     // the Lima release for a Mac host
	KindVMImage = "vm-image" // the base image of a Mac host's VM
)

var kinds = map[string]bool{
	KindHelper: true, KindVT: true, KindUserns: true, KindVMM: true, KindKernel: true, KindTools: true,
	KindImage: true, KindGvisor: true, KindLaunch: true, KindLima: true, KindVMImage: true,
}

// Manifest is one release's manifest.
type Manifest struct {
	// Release is the release's version, such as 0.6.0.
	Release string `json:"release"`
	// Runtime is a label that changes when any runtime item changes.
	Runtime string `json:"runtime,omitempty"`
	// Items are the trusted artefacts.
	Items []Item `json:"items"`
	// Advisories is the known-bad list (isolation spec 20.9).
	Advisories []Advisory `json:"advisories"`
}

// Item is one trusted artefact, for one architecture.
type Item struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256,omitempty"`
	Size    int64  `json:"size,omitempty"`
	// URL is where the release has the file. It always points at the
	// project's own release.
	URL     string `json:"url,omitempty"`
	Licence string `json:"licence,omitempty"`
	// SourceURL is the source of a GPL item, in the same release.
	SourceURL string `json:"source_url,omitempty"`
	// EOL and HostKernel are a VMM's: the date its fixes end, and the
	// host kernels it runs on, such as ">=6.8".
	EOL        string `json:"eol,omitempty"`
	HostKernel string `json:"host_kernel,omitempty"`
	// Released is the upstream release date of a pinned VMM, guest kernel
	// or runsc, as YYYY-MM-DD. host doctor shows each pin's age from it.
	Released string `json:"released,omitempty"`
	// Digest, RootHash and DataBlocks are an image's, in place of Version,
	// SHA256, Size and URL: its OCI manifest digest, and its verity tree.
	// A tools item also carries RootHash, Salt and DataBlocks alongside
	// Version, SHA256, Size and URL (isolation spec 20.1).
	Digest     string `json:"digest,omitempty"`
	RootHash   string `json:"root_hash,omitempty"`
	Salt       string `json:"salt,omitempty"`
	DataBlocks int64  `json:"data_blocks,omitempty"`
}

// Advisory is one entry of the known-bad list (isolation spec 20.9): the
// versions of a VMM, a guest kernel, runsc or a terminal below Before have
// a known advisory. knownbad.go has the match rule, and KnownBad the list.
type Advisory struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Before is the lowest version from which every later version has the
	// fix. An Unresolved entry has none: no release of the item has the
	// fix yet, or its first fixed release is not known, so every version
	// matches it.
	Before     string  `json:"before,omitempty"`
	Unresolved bool    `json:"unresolved,omitempty"`
	Severity   string  `json:"severity,omitempty"`
	CVSS       float64 `json:"cvss,omitempty"`
	Published  string  `json:"published,omitempty"`
	URL        string  `json:"url,omitempty"`
}

var (
	hex64          = regexp.MustCompile(`^[0-9a-f]{64}$`)
	releaseVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	releaseAsset   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ReleaseFolder is the folder of the project's own release of a version,
// with no final slash. Every URL in that release's manifest points into it.
func ReleaseFolder(release string) string {
	return brand.ReleasesURL + "/download/v" + strings.TrimPrefix(release, "v")
}

// IsHex64 reports whether s is a SHA-256 in lower-case hex, as the manifest
// writes it.
func IsHex64(s string) bool { return hex64.MatchString(s) }

// NewItem makes the item of a file: its SHA-256 and size come from data.
func NewItem(kind, name, version, arch string, data []byte, url string) Item {
	sum := sha256.Sum256(data)
	return Item{Kind: kind, Name: name, Version: version, Arch: arch, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), URL: url}
}

// Load reads a manifest and checks its format. It refuses a field it does
// not know, and anything after the one JSON object.
func Load(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("read the release manifest: %w", err)
	}
	var more json.RawMessage
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, errors.New("read the release manifest: it holds more than one JSON object")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Marshal writes a manifest as indented JSON, with a final newline.
func Marshal(m *Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	c := *m
	if c.Advisories == nil {
		c.Advisories = []Advisory{}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("write the release manifest: %w", err)
	}
	return append(b, '\n'), nil
}

// Validate checks the format of isolation spec 14.9.1: each item has the
// fields of its kind, no item is named twice, and every URL points at a
// file in the project's own release of m.Release (ReleaseFolder).
func (m *Manifest) Validate() error {
	if m.Release == "" {
		return errors.New("the release manifest names no release")
	}
	if !releaseVersion.MatchString(m.Release) {
		return fmt.Errorf("the release manifest's release %q is not one URL component", m.Release)
	}
	seen := map[string]bool{}
	folder := ReleaseFolder(m.Release) + "/"
	for _, it := range m.Items {
		if err := it.validate(); err != nil {
			return fmt.Errorf("the release manifest's %s: %w", it.label(), err)
		}
		key := it.Kind + " " + it.Name + " " + it.Arch
		if seen[key] {
			return fmt.Errorf("the release manifest names %s twice", it.label())
		}
		seen[key] = true
		for _, u := range []string{it.URL, it.SourceURL} {
			if name, ok := strings.CutPrefix(u, folder); u != "" && (!ok || !releaseAsset.MatchString(name)) {
				return fmt.Errorf("the release manifest's %s points at %s, outside the project's release at %s", it.label(), u, folder)
			}
		}
	}
	return validateAdvisories(m.Advisories)
}

// Find returns the item of a kind, a name and an architecture.
func (m *Manifest) Find(kind, name, arch string) (Item, bool) {
	for _, it := range m.Items {
		if it.Kind == kind && it.Name == name && it.Arch == arch {
			return it, true
		}
	}
	return Item{}, false
}

func (it Item) label() string {
	return fmt.Sprintf("%s %q for %s", it.Kind, it.Name, it.Arch)
}

func (it Item) validate() error {
	switch {
	case !kinds[it.Kind]:
		return fmt.Errorf("unknown kind %q", it.Kind)
	case it.Name == "":
		return errors.New("no name")
	case !slices.Contains(Arches, it.Arch):
		return fmt.Errorf("unknown architecture %q: the manifest names amd64 and arm64 only", it.Arch)
	case it.Kind == KindImage:
		return it.validateImage()
	case it.Version == "":
		return errors.New("no version")
	case !hex64.MatchString(it.SHA256):
		return fmt.Errorf("sha256 %q is not 64 lower-case hex digits", it.SHA256)
	case it.Size <= 0:
		return errors.New("no size")
	case strings.Contains(it.Licence, "GPL") && it.SourceURL == "":
		return fmt.Errorf("licence %s needs a source_url", it.Licence)
	case it.Kind == KindKernel && it.SourceURL == "":
		return errors.New("a kernel needs a source_url")
	case it.Kind == KindVMM && !isDate(it.EOL):
		return fmt.Errorf("a vmm needs its eol as YYYY-MM-DD, not %q", it.EOL)
	case it.Kind == KindVMM && it.HostKernel == "":
		return errors.New("a vmm needs its host_kernel")
	case pinKinds[it.Kind] && !isDate(it.Released):
		return fmt.Errorf("a %s needs its released date as YYYY-MM-DD, not %q", it.Kind, it.Released)
	case it.Released != "" && !isDate(it.Released):
		return fmt.Errorf("released %q is not a date as YYYY-MM-DD", it.Released)
	}
	if err := it.validateBootItem(); err != nil {
		return err
	}
	if err := checkURL(it.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if it.SourceURL != "" {
		if err := checkURL(it.SourceURL); err != nil {
			return fmt.Errorf("source_url: %w", err)
		}
	}
	return nil
}

// isDate reports whether s is a real date, as YYYY-MM-DD.
func isDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// validateImage checks an image item, which has digest, root_hash and
// data_blocks in place of version, sha256, size and url.
func (it Item) validateImage() error {
	switch {
	case !strings.HasPrefix(it.Digest, "sha256:") || !hex64.MatchString(strings.TrimPrefix(it.Digest, "sha256:")):
		return fmt.Errorf("digest %q is not sha256: and 64 lower-case hex digits", it.Digest)
	case !hex64.MatchString(it.RootHash):
		return fmt.Errorf("root_hash %q is not 64 lower-case hex digits", it.RootHash)
	case it.DataBlocks <= 0:
		return errors.New("no data_blocks")
	case it.DataBlocks > math.MaxInt64/4096:
		return errors.New("data_blocks exceeds the supported byte range")
	case it.Version != "" || it.SHA256 != "" || it.Size != 0 || it.URL != "":
		return errors.New("an image has digest, root_hash and data_blocks in place of version, sha256, size and url")
	}
	return nil
}

// checkURL accepts an https URL of a file, with no query and no fragment.
func checkURL(s string) error {
	u, err := url.Parse(s)
	switch {
	case s == "":
		return errors.New("missing")
	case err != nil:
		return fmt.Errorf("%q: %w", s, err)
	case u.Scheme != "https" || u.Host == "" || u.User != nil:
		return fmt.Errorf("%q is not an https URL", s)
	case u.ForceQuery || u.RawQuery != "" || strings.ContainsAny(s, "#%\\") || strings.HasSuffix(u.Path, "/") || path.Clean(u.Path) != u.Path:
		return fmt.Errorf("%q does not name one file", s)
	}
	return nil
}

// MismatchError says that a file does not match its item.
type MismatchError struct {
	Name string // the file
	What string // "size" or "SHA-256"
	Want string
	Got  string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("%s does not match its %s in the release manifest: %s, not %s", e.Name, e.What, e.Got, e.Want)
}

// Check checks data, the bytes of the file name, against it: its size,
// then its SHA-256.
func Check(it Item, name string, data []byte) error {
	r := bytes.NewReader(data)
	if err := checkReader(it, name, int64(len(data)), r); err != nil {
		return err
	}
	return checkToolsTree(it, r)
}

// CheckFile checks the file at p against it. It checks the file's size
// before it reads it, and its SHA-256 after (isolation spec 14.9.2). A tools
// image must also carry the verity tree and root hash of its data blocks.
func CheckFile(it Item, p string) error {
	f, err := openRegular(p)
	if err != nil {
		return fmt.Errorf("check %s: %w", p, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("check %s: %w", p, err)
	}
	if err := checkReader(it, filepath.Base(p), fi.Size(), f); err != nil {
		return err
	}
	return checkToolsTree(it, f)
}

// checkReader checks a file of size bytes, read from r, against it. It
// reads no more than the item's size and one byte.
func checkReader(it Item, name string, size int64, r io.Reader) error {
	if it.Kind == KindImage || it.SHA256 == "" {
		return fmt.Errorf("%s has no sha256 in the release manifest", it.label())
	}
	if it.Kind == KindTools {
		if err := checkToolsBounds(it); err != nil {
			return err
		}
	}
	if size != it.Size {
		return &MismatchError{Name: name, What: "size", Want: strconv.FormatInt(it.Size, 10) + " bytes", Got: strconv.FormatInt(size, 10) + " bytes"}
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, it.Size+1))
	if err != nil {
		return fmt.Errorf("check %s: %w", name, err)
	}
	if n != it.Size {
		return &MismatchError{Name: name, What: "size", Want: strconv.FormatInt(it.Size, 10) + " bytes", Got: strconv.FormatInt(n, 10) + " bytes"}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != it.SHA256 {
		return &MismatchError{Name: name, What: "SHA-256", Want: it.SHA256, Got: got}
	}
	return nil
}
