package image

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/klauspost/compress/zstd"
)

type flatBudget struct {
	compressed, uncompressed int64
}

type flatContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *flatContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type flatCapReader struct {
	r         io.Reader
	remaining *int64
	label     string
}

func (r *flatCapReader) Read(p []byte) (int, error) {
	if *r.remaining < 0 {
		return 0, tooLarge("the image exceeds the %s cap", r.label)
	}
	if int64(len(p)) > *r.remaining+1 {
		p = p[:*r.remaining+1]
	}
	n, err := r.r.Read(p)
	*r.remaining -= int64(n)
	if *r.remaining < 0 {
		return n, tooLarge("the image exceeds the %s cap", r.label)
	}
	return n, err
}

type flatLayerReader struct {
	r         io.Reader
	remaining int64
}

func (r *flatLayerReader) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return 0, badLayer("a layer exceeds its manifest size")
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n, badLayer("a layer exceeds its manifest size")
	}
	return n, err
}

func layerDecoder(media string, r io.Reader) (io.Reader, func(), error) {
	switch media {
	case "application/vnd.oci.image.layer.v1.tar":
		return r, func() {}, nil
	case "application/vnd.oci.image.layer.v1.tar+gzip", "application/vnd.docker.image.rootfs.diff.tar.gzip":
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, flatReadError(err)
		}
		return gz, func() { _ = gz.Close() }, nil
	case "application/vnd.oci.image.layer.v1.tar+zstd":
		// In streaming mode MaxMemory bounds the history window, not total
		// decoded bytes. The reader below counts the full 16 GiB stream cap.
		// Lowmem allocates the history window plus a block buffer; the whole
		// process still has its independent 1 GiB systemd memory boundary.
		zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(1<<30), zstd.WithDecoderMaxWindow(1<<30))
		if err != nil {
			return nil, nil, flatReadError(err)
		}
		return zr, zr.Close, nil
	default:
		return nil, nil, badLayer("unsupported layer media type %q", media)
	}
}

func (t *flatTree) readLayer(ctx context.Context, layer Layer, input io.Reader, number int, budget *flatBudget) error {
	if layer.Size < 0 {
		return badLayer("a layer has a negative manifest size")
	}
	if layer.Size > budget.compressed {
		return tooLarge("the image exceeds the compressed layers cap")
	}
	raw := &flatLayerReader{r: &flatContextReader{ctx, input}, remaining: layer.Size}
	compressed := &flatCapReader{raw, &budget.compressed, "compressed layers"}
	decoded, closeDecoder, err := layerDecoder(layer.MediaType, compressed)
	if err != nil {
		return err
	}
	defer closeDecoder()
	uncompressed := &flatCapReader{decoded, &budget.uncompressed, "uncompressed layers"}
	tr := tar.NewReader(uncompressed)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return flatReadError(err)
		}
		if err := t.readEntry(tr, h, number); err != nil {
			return err
		}
	}
	// tar stops at its terminator. Draining validates compression trailers and
	// counts all remaining bytes, including data after the terminator.
	if _, err := io.Copy(io.Discard, uncompressed); err != nil {
		return flatReadError(err)
	}
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return flatReadError(err)
	}
	if raw.remaining != 0 {
		return badLayer("a layer is shorter than its manifest size")
	}
	return nil
}

func (t *flatTree) readEntry(tr *tar.Reader, h *tar.Header, layer int) error {
	name, err := flatName(h.Name)
	if err != nil {
		return err
	}
	switch h.Typeflag {
	case tar.TypeDir, tar.TypeReg, tar.TypeRegA, tar.TypeLink, tar.TypeSymlink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock:
	default:
		return badLayer("unsupported layer entry type %d at %q", h.Typeflag, name)
	}
	if whiteout, err := t.whiteout(name, layer); whiteout || err != nil {
		return err
	}
	if h.Typeflag == tar.TypeChar || h.Typeflag == tar.TypeBlock {
		t.devices++
		return t.drop(name)
	}
	if err := flatMetadata(h); err != nil {
		return err
	}
	node := &flatNode{header: normalHeader(h, name), layer: layer}
	switch h.Typeflag {
	case tar.TypeDir:
		node.header.Size = 0
		node.children = map[string]*flatNode{}
	case tar.TypeReg, tar.TypeRegA:
		if h.Size < 0 {
			return badLayer("a layer file has a negative size")
		}
		if h.Size > t.limits.uncompressed-t.stored {
			return tooLarge("the image exceeds the uncompressed file data cap")
		}
		node.content = &flatContent{typ: tar.TypeReg, offset: t.stored, size: h.Size}
		w := &flatScratchWriter{scratch: t.scratch, offset: t.stored}
		if _, err := io.CopyN(w, tr, h.Size); err != nil {
			var scratchErr *flatScratchError
			if errors.As(err, &scratchErr) {
				return err
			}
			return flatReadError(err)
		}
		t.stored += h.Size
	case tar.TypeSymlink, tar.TypeFifo:
		if strings.ContainsRune(h.Linkname, 0) || len(h.Linkname) > 4096 {
			return badLayer("a layer link target is longer than 4096 bytes or contains a NUL byte")
		}
		node.content = &flatContent{typ: h.Typeflag, link: h.Linkname}
		node.header.Size = 0
	case tar.TypeLink:
		target, err := flatName(h.Linkname)
		if err != nil {
			return err
		}
		original := t.find(target)
		if original == nil || original.children != nil {
			return badLayer("hard link %q names a missing file or a directory: %q", name, h.Linkname)
		}
		node.content = original.content
		node.header.Size = 0
	default:
		return badLayer("unsupported layer entry type %d at %q", h.Typeflag, name)
	}
	return t.put(name, node, layer)
}

// flatMetadata refuses numbers that ext4 cannot represent. In particular,
// tar2ext4 casts IDs to uint32 and modes to uint16 without checking them.
// The pinned converter has its own symlink, hardlink and timestamp encoding
// semantics; this check never clips a header or changes those semantics.
func flatMetadata(h *tar.Header) error {
	const maxID = uint64(1<<32 - 1)
	if h.Uid < 0 || uint64(h.Uid) > maxID || h.Gid < 0 || uint64(h.Gid) > maxID {
		return badLayer("entry %q has an owner or group outside ext4's range of 0 to 4294967295", h.Name)
	}
	if h.Mode < 0 || h.Mode > 0o7777 {
		return badLayer("entry %q has a mode outside ext4's permission range of 0000 to 7777", h.Name)
	}
	seconds := h.ModTime.Unix()
	if seconds < -0x80000000 || seconds > 0x37fffffff {
		return badLayer("entry %q has a modification time outside ext4's range of -2147483648 to 15032385535 Unix seconds", h.Name)
	}
	return nil
}

type flatScratchWriter struct {
	scratch flattenScratch
	offset  int64
}

func (w *flatScratchWriter) Write(p []byte) (int, error) {
	n, err := w.scratch.WriteAt(p, w.offset)
	w.offset += int64(n)
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		return n, &flatScratchError{err}
	}
	return n, nil
}

type flatScratchError struct{ err error }

func (e *flatScratchError) Error() string {
	return fmt.Sprintf("write the image scratch extent: %v", e.err)
}
func (e *flatScratchError) Unwrap() error { return e.err }

func flatReadError(err error) error {
	var pe *proto.Error
	if err == nil || errors.As(err, &pe) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return badLayer("read an image layer: %v", err)
}
