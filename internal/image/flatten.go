package image

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Spec 20.2 caps for image pulls and flattening.
const (
	MaxManifestBytes     int64 = 4 << 20   // 4 MiB
	MaxLayers                  = 128       // 128 layers
	MaxCompressedBytes   int64 = 8 << 30   // 8 GiB
	MaxUncompressedBytes int64 = 16 << 30  // 16 GiB (tar2ext4 default maximum)
	MaxEntries           int64 = 1_000_000 // 1,000,000 entries
	MaxPathBytes               = 4096      // 4,096 bytes per entry name or link target
)

// Limits bounds image flattening. Zero fields use the spec 20.2 defaults.
type Limits struct {
	MaxManifestBytes     int64
	MaxLayers            int
	MaxCompressedBytes   int64
	MaxUncompressedBytes int64
	MaxEntries           int64
	MaxPathBytes         int
}

func (l Limits) withDefaults() Limits {
	if l.MaxManifestBytes <= 0 {
		l.MaxManifestBytes = MaxManifestBytes
	}
	if l.MaxLayers <= 0 {
		l.MaxLayers = MaxLayers
	}
	if l.MaxCompressedBytes <= 0 {
		l.MaxCompressedBytes = MaxCompressedBytes
	}
	if l.MaxUncompressedBytes <= 0 {
		l.MaxUncompressedBytes = MaxUncompressedBytes
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = MaxEntries
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = MaxPathBytes
	}
	return l
}

// FlattenStats summarises a flattened image stream.
type FlattenStats struct {
	Layers            int
	Entries           int64
	UncompressedBytes int64
	DroppedDevices    int64
}

// AllowedLayerMediaType reports whether mt is an accepted OCI or Docker layer
// media type under spec 20.2 rule 1.
func AllowedLayerMediaType(mt types.MediaType) bool {
	switch mt {
	case types.OCIUncompressedLayer, types.OCILayer, types.OCILayerZStd, types.DockerLayer:
		return true
	default:
		return false
	}
}

// Flatten validates img against spec 20.2, flattens its layers with
// go-containerregistry's mutate.Extract, and writes a normalised PAX tar stream
// to w.
func Flatten(img v1.Image, ref string, lim Limits, w io.Writer) (FlattenStats, error) {
	lim = lim.withDefaults()
	var stats FlattenStats

	if raw, err := img.RawManifest(); err == nil && int64(len(raw)) > lim.MaxManifestBytes {
		return stats, badLayer(ref, fmt.Sprintf("manifest is %d bytes, over the %d byte limit", len(raw), lim.MaxManifestBytes), nil)
	}

	layers, err := img.Layers()
	if err != nil {
		return stats, badLayer(ref, "could not read image layers", err)
	}
	if len(layers) > lim.MaxLayers {
		return stats, tooLarge(ref, fmt.Sprintf("image has %d layers, over the %d layer limit", len(layers), lim.MaxLayers))
	}
	stats.Layers = len(layers)

	var totalComp int64
	for i, layer := range layers {
		mt, err := layer.MediaType()
		if err != nil {
			return stats, badLayer(ref, fmt.Sprintf("layer %d media type: %v", i, err), err)
		}
		if !AllowedLayerMediaType(mt) {
			return stats, badLayer(ref, fmt.Sprintf("layer %d has unsupported media type %q", i, mt), nil)
		}
		sz, err := layer.Size()
		if err != nil {
			return stats, badLayer(ref, fmt.Sprintf("layer %d size: %v", i, err), err)
		}
		if sz < 0 {
			return stats, badLayer(ref, fmt.Sprintf("layer %d has negative size %d", i, sz), nil)
		}
		totalComp += sz
		if totalComp > lim.MaxCompressedBytes {
			return stats, tooLarge(ref, fmt.Sprintf("compressed layers exceed %d bytes", lim.MaxCompressedBytes))
		}
	}

	wrapped := &cappedImage{
		Image:      img,
		ref:        ref,
		compCap:    &byteBudget{limit: lim.MaxCompressedBytes, ref: ref, label: "compressed layers"},
		uncompCap:  &byteBudget{limit: lim.MaxUncompressedBytes, ref: ref, label: "uncompressed layers"},
		origLayers: layers,
	}

	rc := mutate.Extract(wrapped)
	defer rc.Close()

	filtered, err := FilterTar(rc, w, ref, lim)
	filtered.Layers = stats.Layers
	if err != nil {
		return filtered, err
	}
	return filtered, nil
}

// FilterTar reads a tar stream (such as mutate.Extract's output), enforces
// spec 20.2's path, entry-type, hard-link, metadata and cap rules, and writes
// the normalised PAX tar stream to w.
func FilterTar(r io.Reader, w io.Writer, ref string, lim Limits) (FlattenStats, error) {
	lim = lim.withDefaults()
	var stats FlattenStats

	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	seenTargets := make(map[string]bool)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, classifyExtractError(ref, err)
		}
		if err := copyFilteredEntry(tr, tw, hdr, lim, ref, &stats, seenTargets); err != nil {
			return stats, err
		}
	}

	// Drain any remaining bytes from r (such as after tar EOF zero blocks).
	// go-containerregistry's mutate.Extract runs `defer tarWriter.Close()` before
	// closing the pipe with its error, so without draining r a layer error (for
	// example an unsafe path, digest mismatch or byte cap) would be hidden behind
	// the synthetic tar EOF trailer.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return stats, classifyExtractError(ref, err)
	}

	if err := tw.Close(); err != nil {
		return stats, fmt.Errorf("finish tar stream: %w", err)
	}
	return stats, nil
}

func copyFilteredEntry(tr *tar.Reader, tw *tar.Writer, hdr *tar.Header, lim Limits, ref string, stats *FlattenStats, seenTargets map[string]bool) error {
	cleanName, err := cleanEntryPath(hdr.Name, lim.MaxPathBytes, ref)
	if err != nil {
		return err
	}
	if cleanName == "." && hdr.Typeflag != tar.TypeDir {
		return badLayer(ref, fmt.Sprintf("root entry %q is not a directory", hdr.Name), nil)
	}
	if hdr.Typeflag == tar.TypeChar || hdr.Typeflag == tar.TypeBlock {
		stats.DroppedDevices++
		return nil
	}

	outHdr, err := normaliseHeader(hdr, cleanName, lim.MaxPathBytes, ref, seenTargets)
	if err != nil {
		return err
	}
	if err := accountEntry(stats, lim, ref, cleanName, outHdr.Size); err != nil {
		return err
	}
	if err := tw.WriteHeader(outHdr); err != nil {
		return fmt.Errorf("write tar header %q: %w", cleanName, err)
	}
	if outHdr.Size > 0 {
		if _, err := io.CopyN(tw, tr, outHdr.Size); err != nil {
			return classifyExtractError(ref, err)
		}
	}
	if outHdr.Typeflag != tar.TypeDir {
		seenTargets[cleanName] = true
	}
	return nil
}

func accountEntry(stats *FlattenStats, lim Limits, ref, cleanName string, size int64) error {
	stats.Entries++
	if stats.Entries > lim.MaxEntries {
		return tooLarge(ref, fmt.Sprintf("flattened tree exceeds %d entries", lim.MaxEntries))
	}
	if size < 0 {
		return badLayer(ref, fmt.Sprintf("entry %q has negative size %d", cleanName, size), nil)
	}
	if size > lim.MaxUncompressedBytes-stats.UncompressedBytes {
		return tooLarge(ref, fmt.Sprintf("unpacks to more than %d bytes", lim.MaxUncompressedBytes))
	}
	stats.UncompressedBytes += size
	return nil
}

func normaliseHeader(hdr *tar.Header, cleanName string, maxPath int, ref string, seenTargets map[string]bool) (*tar.Header, error) {
	tf := hdr.Typeflag
	if tf == 0 {
		tf = tar.TypeReg
	}
	switch tf {
	case tar.TypeReg, tar.TypeDir, tar.TypeSymlink, tar.TypeLink, tar.TypeFifo:
	default:
		return nil, badLayer(ref, fmt.Sprintf("entry %q has unsupported tar type %q", cleanName, tf), nil)
	}

	linkName := hdr.Linkname
	switch tf {
	case tar.TypeLink:
		target, err := cleanEntryPath(linkName, maxPath, ref)
		if err != nil {
			return nil, err
		}
		if !seenTargets[target] {
			return nil, badLayer(ref, fmt.Sprintf("hard link %q names a missing target %q", cleanName, target), nil)
		}
		linkName = target
	case tar.TypeSymlink:
		if strings.IndexByte(linkName, 0) >= 0 {
			return nil, badLayer(ref, fmt.Sprintf("symlink %q has a NUL byte in its target", cleanName), nil)
		}
		if len(linkName) > maxPath {
			return nil, badLayer(ref, fmt.Sprintf("symlink %q target exceeds %d bytes", cleanName, maxPath), nil)
		}
	default:
		linkName = ""
	}

	size := hdr.Size
	if tf != tar.TypeReg {
		size = 0
	}

	out := &tar.Header{
		Typeflag:   tf,
		Name:       cleanName,
		Linkname:   linkName,
		Size:       size,
		Mode:       hdr.Mode,
		Uid:        hdr.Uid,
		Gid:        hdr.Gid,
		ModTime:    hdr.ModTime,
		Format:     tar.FormatPAX,
		PAXRecords: filterPAXRecords(hdr.PAXRecords),
	}
	// AccessTime, ChangeTime, Uname, Gname are zeroed per spec 20.2 rule 7.
	out.AccessTime = time.Time{}
	out.ChangeTime = time.Time{}
	return out, nil
}

func filterPAXRecords(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	var out map[string]string
	for k, v := range in {
		if !strings.HasPrefix(k, "SCHILY.xattr.") {
			continue
		}
		if strings.HasPrefix(k, "SCHILY.xattr.trusted.") {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[k] = v
	}
	return out
}

func cleanEntryPath(raw string, maxPath int, ref string) (string, error) {
	if strings.IndexByte(raw, 0) >= 0 {
		return "", badLayer(ref, fmt.Sprintf("path %q contains a NUL byte", raw), nil)
	}
	if len(raw) > maxPath {
		return "", badLayer(ref, fmt.Sprintf("path exceeds %d bytes", maxPath), nil)
	}
	if hasDotDotSegment(raw) {
		return "", badLayer(ref, fmt.Sprintf("path leaves the image: %q", raw), nil)
	}
	trimmed := raw
	for {
		switch {
		case strings.HasPrefix(trimmed, "/"):
			trimmed = strings.TrimPrefix(trimmed, "/")
		case strings.HasPrefix(trimmed, "./"):
			trimmed = strings.TrimPrefix(trimmed, "./")
		default:
			goto doneTrim
		}
	}
doneTrim:
	if trimmed == "" || trimmed == "." {
		return ".", nil
	}
	clean := path.Clean(trimmed)
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", badLayer(ref, fmt.Sprintf("path leaves the image: %q", raw), nil)
	}
	if len(clean) > maxPath {
		return "", badLayer(ref, fmt.Sprintf("path exceeds %d bytes", maxPath), nil)
	}
	return clean, nil
}

func hasDotDotSegment(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func classifyExtractError(ref string, err error) error {
	if ie, ok := AsError(err); ok {
		return ie
	}
	msg := err.Error()
	if strings.Contains(msg, "verifying layer") || strings.Contains(msg, "digest") {
		return digestMismatch(ref, msg, err)
	}
	return badLayer(ref, msg, err)
}

type byteBudget struct {
	mu    sync.Mutex
	used  int64
	limit int64
	ref   string
	label string
}

func (b *byteBudget) add(n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used += n
	if b.used > b.limit {
		return tooLarge(b.ref, fmt.Sprintf("%s exceed %d bytes", b.label, b.limit))
	}
	return nil
}

type budgetReadCloser struct {
	rc     io.ReadCloser
	budget *byteBudget
}

func (r *budgetReadCloser) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 {
		if berr := r.budget.add(int64(n)); berr != nil {
			return 0, berr
		}
	}
	if err != nil {
		return n, err //nolint:wrapcheck // preserves io.EOF and underlying stream error for tar.Reader
	}
	return n, nil
}

func (r *budgetReadCloser) Close() error {
	if err := r.rc.Close(); err != nil {
		return fmt.Errorf("close layer stream: %w", err)
	}
	return nil
}

type cappedImage struct {
	v1.Image
	ref        string
	compCap    *byteBudget
	uncompCap  *byteBudget
	origLayers []v1.Layer
}

func (c *cappedImage) Layers() ([]v1.Layer, error) {
	out := make([]v1.Layer, len(c.origLayers))
	for i, l := range c.origLayers {
		out[i] = &cappedLayer{Layer: l, compCap: c.compCap, uncompCap: c.uncompCap}
	}
	return out, nil
}

type cappedLayer struct {
	v1.Layer
	compCap   *byteBudget
	uncompCap *byteBudget
}

func (l *cappedLayer) Compressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Compressed()
	if err != nil {
		return nil, fmt.Errorf("open compressed layer: %w", err)
	}
	return &budgetReadCloser{rc: rc, budget: l.compCap}, nil
}

func (l *cappedLayer) Uncompressed() (io.ReadCloser, error) {
	rc, err := l.Layer.Uncompressed()
	if err != nil {
		return nil, fmt.Errorf("open uncompressed layer: %w", err)
	}
	return &budgetReadCloser{rc: rc, budget: l.uncompCap}, nil
}
