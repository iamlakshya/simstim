package image

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// flattenScratch is an unlinked, descriptor-owned arena. The converter maps
// this interface onto a disjoint extent of its fresh output descriptor.
type flattenScratch interface {
	io.ReaderAt
	io.WriterAt
}

type flatContent struct {
	typ    byte
	link   string
	offset int64
	size   int64
}

type flatNode struct {
	header   *tar.Header
	content  *flatContent
	layer    int
	cleared  int // this layer already removed every lower descendant
	children map[string]*flatNode
}

type flatTree struct {
	root    *flatNode
	entries int
	devices int
	scratch flattenScratch
	stored  int64
	limits  flattenLimits
}

type flattenLimits struct {
	compressed, uncompressed int64
	entries, layers          int
}

func defaultFlattenLimits() flattenLimits {
	return flattenLimits{MaxCompressed, MaxUncompressed, MaxEntries, MaxLayers}
}

// Flatten applies verified OCI layers and writes one normalised PAX tar. It
// never follows an image path on the host. File bodies live in scratch rather
// than memory, and hard links retain their content after a target is removed.
// Scratch must not overlap out or the converted filesystem's logical extent.
func Flatten(ctx context.Context, layers []Layer, readers []io.Reader, scratch flattenScratch, out io.Writer) (int, error) {
	return flattenWithLimits(ctx, layers, readers, scratch, out, defaultFlattenLimits())
}

func flattenWithLimits(ctx context.Context, layers []Layer, readers []io.Reader, scratch flattenScratch, out io.Writer, limits flattenLimits) (int, error) {
	if len(layers) != len(readers) || scratch == nil || out == nil {
		return 0, badLayer("the converter's layer descriptors are incomplete")
	}
	if len(layers) > limits.layers {
		return 0, tooLarge("the image has more than %d layers", limits.layers)
	}
	root := newFlatDir(".", 0)
	if limits.entries < 1 {
		return 0, tooLarge("the flattened image has no room for its root directory")
	}
	tree := &flatTree{root: root, entries: 1, scratch: scratch, limits: limits}
	budget := &flatBudget{compressed: limits.compressed, uncompressed: limits.uncompressed}
	for i, layer := range layers {
		if readers[i] == nil {
			return tree.devices, badLayer("layer %d has no descriptor", i+1)
		}
		if err := tree.readLayer(ctx, layer, readers[i], i+1, budget); err != nil {
			return tree.devices, err
		}
	}
	tw := tar.NewWriter(out)
	seen := map[*flatContent]string{}
	if err := tree.emit(ctx, tw, tree.root, ".", seen); err != nil {
		return tree.devices, err
	}
	if err := tw.Close(); err != nil {
		return tree.devices, fmt.Errorf("finish the normalised image tar: %w", err)
	}
	return tree.devices, nil
}

func newFlatDir(name string, layer int) *flatNode {
	return &flatNode{
		header: &tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Unix(0, 0), Format: tar.FormatPAX},
		layer:  layer, children: map[string]*flatNode{},
	}
}

func flatName(raw string) (string, error) {
	if len(raw) > 4096 || strings.ContainsRune(raw, 0) {
		return "", badLayer("a layer path is longer than 4096 bytes or contains a NUL byte")
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", badLayer("a layer path leaves the image: %q", raw)
		}
	}
	// Remove all leading slashes and ./ prefixes before cleaning. No host
	// pathname is ever made from this result.
	for strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "./") {
		raw = strings.TrimPrefix(strings.TrimPrefix(raw, "/"), "./")
	}
	name := path.Clean(raw)
	if name == "" {
		name = "."
	}
	return name, nil
}

func normalHeader(h *tar.Header, name string) *tar.Header {
	n := &tar.Header{Name: name, Typeflag: h.Typeflag, Linkname: h.Linkname,
		Mode: h.Mode, Uid: h.Uid, Gid: h.Gid, Size: h.Size,
		ModTime: h.ModTime, Format: tar.FormatPAX}
	for key, value := range h.PAXRecords {
		const prefix = "SCHILY.xattr."
		if strings.HasPrefix(key, prefix) && !strings.HasPrefix(strings.TrimPrefix(key, prefix), "trusted.") {
			if n.PAXRecords == nil {
				n.PAXRecords = map[string]string{}
			}
			n.PAXRecords[key] = value
		}
	}
	return n
}

func (t *flatTree) find(name string) *flatNode {
	n := t.root
	if name == "." {
		return n
	}
	for _, part := range strings.Split(name, "/") {
		n = n.children[part]
		if n == nil {
			return nil
		}
	}
	return n
}

func (t *flatTree) parent(name string, layer int) (*flatNode, string, error) {
	parts := strings.Split(name, "/")
	n := t.root
	for i, part := range parts[:len(parts)-1] {
		child := n.children[part]
		if child == nil {
			child = newFlatDir(strings.Join(parts[:i+1], "/"), layer)
			n.children[part] = child
			t.entries++
			if t.entries > t.limits.entries {
				return nil, "", tooLarge("the flattened image has more than %d entries", t.limits.entries)
			}
		}
		if child.children == nil {
			return nil, "", badLayer("a layer path has a non-directory parent: %q", name)
		}
		n = child
	}
	return n, parts[len(parts)-1], nil
}

func (t *flatTree) put(name string, node *flatNode, layer int) error {
	if name == "." {
		if node.children == nil {
			return badLayer("the image root is not a directory")
		}
		node.children = t.root.children
		t.root = node
		return nil
	}
	parent, base, err := t.parent(name, layer)
	if err != nil {
		return err
	}
	old := parent.children[base]
	if old == nil {
		t.entries++
	} else if old.children != nil && node.children != nil {
		node.children = old.children
		node.cleared = old.cleared
	} else {
		t.entries -= flatDescendants(old)
	}
	if t.entries > t.limits.entries {
		return tooLarge("the flattened image has more than %d entries", t.limits.entries)
	}
	parent.children[base] = node
	return nil
}

func flatDescendants(node *flatNode) int {
	n := 0
	for _, child := range node.children {
		n += 1 + flatDescendants(child)
	}
	return n
}

// removeLower preserves this layer's entries even when its whiteout follows
// them in the tar. A remaining upper child gets an implicit directory parent.
func (t *flatTree) removeLower(parent *flatNode, base string, layer int) {
	node := parent.children[base]
	if node == nil {
		return
	}
	if node.cleared != layer {
		for name := range node.children {
			t.removeLower(node, name, layer)
		}
		node.cleared = layer
	}
	if node.layer >= layer {
		return
	}
	if len(node.children) > 0 {
		replacement := newFlatDir(node.header.Name, layer)
		replacement.children = node.children
		replacement.cleared = layer
		parent.children[base] = replacement
		return
	}
	delete(parent.children, base)
	t.entries--
}

func (t *flatTree) whiteout(name string, layer int) (bool, error) {
	base := path.Base(name)
	if !strings.HasPrefix(base, ".wh.") {
		return false, nil
	}
	target := strings.TrimPrefix(base, ".wh.")
	if target == "" || target == "." || target == ".." {
		return true, badLayer("a whiteout has an invalid target: %q", name)
	}
	dir := t.find(path.Dir(name))
	if dir == nil {
		return true, nil
	}
	if dir.children == nil {
		return true, badLayer("a whiteout has a non-directory parent: %q", name)
	}
	if base == ".wh..wh..opq" {
		if dir.cleared != layer {
			for child := range dir.children {
				t.removeLower(dir, child, layer)
			}
			dir.cleared = layer
		}
	} else {
		t.removeLower(dir, target, layer)
	}
	return true, nil
}

func (t *flatTree) drop(name string) error {
	if name == "." {
		return badLayer("the image root cannot be a device")
	}
	parent := t.find(path.Dir(name))
	if parent == nil {
		return nil
	}
	if parent.children == nil {
		return badLayer("a layer device has a non-directory parent: %q", name)
	}
	if old := parent.children[path.Base(name)]; old != nil {
		t.entries -= 1 + flatDescendants(old)
		delete(parent.children, path.Base(name))
	}
	return nil
}

func (t *flatTree) emit(ctx context.Context, tw *tar.Writer, node *flatNode, name string, seen map[*flatContent]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h := *node.header
	h.Name = name
	if node.children == nil {
		if first, ok := seen[node.content]; ok {
			h.Typeflag, h.Linkname, h.Size = tar.TypeLink, first, 0
		} else {
			seen[node.content] = name
			h.Typeflag, h.Linkname, h.Size = node.content.typ, node.content.link, node.content.size
		}
	}
	if err := tw.WriteHeader(&h); err != nil {
		return badLayer("write normalised entry %q: %v", name, err)
	}
	if h.Typeflag == tar.TypeReg && h.Size != 0 {
		r := io.NewSectionReader(t.scratch, node.content.offset, h.Size)
		if _, err := io.CopyN(tw, &flatContextReader{ctx, r}, h.Size); err != nil {
			return fmt.Errorf("write normalised image body: %w", err)
		}
	}
	names := make([]string, 0, len(node.children))
	for child := range node.children {
		names = append(names, child)
	}
	sort.Strings(names)
	for _, child := range names {
		childName := child
		if name != "." {
			childName = name + "/" + child
		}
		if err := t.emit(ctx, tw, node.children[child], childName, seen); err != nil {
			return err
		}
	}
	return nil
}
