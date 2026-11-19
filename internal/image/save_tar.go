package image

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
	"github.com/iamlakshya/simstim/internal/proto"
)

// SavedRootReport contains public findings only, never scanner values.
type SavedRootReport struct {
	Flagged        []SaveFinding `json:"flagged"`
	Excluded       []SaveFinding `json:"excluded"`
	DroppedDevices int           `json:"dropped_devices"`
}

// NormaliseSavedRoot checks a raw complete-root tar independently of the
// guest's review list. It is an exporter input, never an OCI layer import.
// Scratch is descriptor-owned and disjoint from output, as in Convert.
func NormaliseSavedRoot(ctx context.Context, plan SavedRootPlan, input io.Reader, scratch flattenScratch, output io.Writer) (SavedRootReport, error) {
	filter, err := newSavedRootFilter(plan)
	if err != nil {
		return SavedRootReport{}, err
	}
	if scratch == nil || input == nil || output == nil {
		return SavedRootReport{}, saveFailure("image_bad_layer", "the saved-root converter has incomplete descriptors")
	}
	scanner, err := newSaveScanner(plan.Values)
	if err != nil {
		return SavedRootReport{}, err
	}
	defer scanner.clear()
	// Advisory and predeclared metadata is public too. A value embedded in a
	// filename must not escape through review, exclusions or an error's path.
	for _, name := range plan.Changes {
		scanner.reset("[changes metadata]")
		if _, err := scanner.Write([]byte(name)); err != nil {
			return SavedRootReport{}, err
		}
	}
	for _, exclusion := range filter.exclusions {
		scanner.reset("[excluded path metadata]")
		if _, err := scanner.Write([]byte(exclusion.Path)); err != nil {
			return SavedRootReport{}, err
		}
	}
	budget := int64(MaxUncompressed)
	bounded := &flatCapReader{&flatContextReader{ctx, input}, &budget, "saved root tar"}
	reader := tar.NewReader(bounded)
	tree := &flatTree{root: newFlatDir(".", 0), entries: 1, scratch: scratch, limits: defaultFlattenLimits()}
	modes := map[string]int64{}
	seen := map[string]bool{}
	report := SavedRootReport{Excluded: slices.Clone(filter.exclusions)}
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return SavedRootReport{}, err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return SavedRootReport{}, savedRootReadError(err)
		}
		if count >= MaxEntries {
			return SavedRootReport{}, tooLarge("the saved root tar has more than %d entries", MaxEntries)
		}
		// Scan metadata before any diagnostic can display its name. This also
		// prevents a secret in a symlink or extended attribute escaping the scan.
		for _, text := range append([]string{header.Name, header.Linkname}, savedXattrs(header)...) {
			scanner.reset("[entry metadata]")
			if _, err := scanner.Write([]byte(text)); err != nil {
				return SavedRootReport{}, err
			}
		}
		name, err := flatName(header.Name)
		if err != nil || name != "." && !savedAbsolutePath("/"+name) {
			return SavedRootReport{}, saveFailure("image_bad_layer", "the saved root tar contains an unsafe path")
		}
		// Cleaning can join pieces separated in the raw tar spelling. Check the
		// exact derived display/export name before it can enter a diagnostic.
		scanner.reset("[canonical entry metadata]")
		if _, err := scanner.Write([]byte("/" + name)); err != nil {
			return SavedRootReport{}, err
		}
		if seen[name] {
			return SavedRootReport{}, saveFailure("image_bad_layer", "the saved root tar repeats an entry")
		}
		seen[name], modes["/"+name] = true, header.Mode
		if err := flatMetadata(header); err != nil {
			return SavedRootReport{}, err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Size != 0 {
			return SavedRootReport{}, saveFailure("image_bad_layer", "a saved non-regular entry has a file body")
		}
		reason := filter.exclude("/" + name)
		if header.Typeflag == tar.TypeChar || header.Typeflag == tar.TypeBlock {
			reason = "device node"
			report.DroppedDevices++
		}
		content := &flatContent{typ: header.Typeflag, link: header.Linkname, offset: tree.stored, size: header.Size}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			content.typ = tar.TypeReg
			scanner.reset("/" + name)
			var sink io.Writer = scanner
			if reason == "" {
				if header.Size < 0 || header.Size > MaxUncompressed-tree.stored {
					return SavedRootReport{}, tooLarge("saved root content exceeds 16 GiB")
				}
				sink = io.MultiWriter(scanner, io.NewOffsetWriter(scratch, tree.stored))
			}
			if _, err := io.CopyN(sink, &flatContextReader{ctx, reader}, header.Size); err != nil {
				return SavedRootReport{}, err
			}
			if reason == "" {
				tree.stored += header.Size
			}
		case tar.TypeLink:
			target, err := flatName(header.Linkname)
			if err != nil || target == "." || !savedAbsolutePath("/"+target) {
				return SavedRootReport{}, saveFailure("image_bad_layer", "the saved root tar contains an unsafe hard link")
			}
			if filter.exclude("/"+target) != "" {
				reason = "hard link to an excluded path"
			}
			if reason == "" {
				original := tree.find(target)
				if original == nil || original.children != nil || original.content == nil {
					return SavedRootReport{}, saveFailure("image_bad_layer", "the saved root tar has an unresolved hard link")
				}
				content = original.content
			}
		case tar.TypeDir, tar.TypeSymlink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock:
		default:
			return SavedRootReport{}, saveFailure("image_bad_layer", "the saved root tar has an unsupported entry type")
		}
		if reason != "" {
			report.Excluded = append(report.Excluded, SaveFinding{Path: "/" + name, Reason: reason})
			continue
		}
		normal := normalHeader(header, name)
		node := &flatNode{header: normal, content: content, layer: 1}
		if header.Typeflag == tar.TypeDir {
			node.children, node.content = map[string]*flatNode{}, nil
		}
		if err := tree.put(name, node, 1); err != nil {
			return SavedRootReport{}, err
		}
	}
	// A malicious trailing payload still consumes the complete byte cap.
	if _, err := io.Copy(io.Discard, bounded); err != nil {
		return SavedRootReport{}, err
	}
	report.Flagged, err = filter.review(plan.Changes, modes)
	if err != nil {
		return SavedRootReport{}, err
	}
	report.Excluded = squashSaveFindings(report.Excluded)
	writer := tar.NewWriter(output)
	if err := tree.emit(ctx, writer, tree.root, ".", map[*flatContent]string{}); err != nil {
		return SavedRootReport{}, err
	}
	if err := writer.Close(); err != nil {
		return SavedRootReport{}, err
	}
	return report, nil
}

func savedRootReadError(err error) error {
	var known *proto.Error
	if errors.As(err, &known) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Archive parsers can put an untrusted malformed field in their error.
	// It has not yet passed the metadata scan, so do not expose that cause.
	return saveFailure("image_bad_layer", "the saved root archive is malformed or unreadable")
}

func savedXattrs(header *tar.Header) []string {
	var values []string
	for name, value := range header.PAXRecords {
		if strings.HasPrefix(name, "SCHILY.xattr.") {
			values = append(values, name, value)
		}
	}
	return values
}

// ConvertSavedRoot runs in the same converter jail as image conversion. It
// consumes exactly one raw root descriptor and writes bounded ext4+verity.
func ConvertSavedRoot(ctx context.Context, plan SavedRootPlan, input io.Reader, output *os.File) (SavedRootReport, error) {
	if input == nil || output == nil {
		return SavedRootReport{}, saveFailure("image_bad_layer", "the saved-root converter has no input or output")
	}
	if err := output.Truncate(0); err != nil {
		return SavedRootReport{}, err
	}
	reader, writer := io.Pipe()
	type normalised struct {
		report SavedRootReport
		err    error
	}
	done := make(chan normalised, 1)
	go func() {
		report, err := NormaliseSavedRoot(ctx, plan, input, scratchFile{output}, writer)
		_ = writer.CloseWithError(err)
		done <- normalised{report, err}
	}()
	view := &virtualFile{file: output}
	err := tar2ext4.Convert(reader, view, tar2ext4.AppendDMVerity)
	_ = reader.CloseWithError(err)
	result := <-done
	if result.err != nil {
		return SavedRootReport{}, result.err
	}
	if err != nil {
		return SavedRootReport{}, fmt.Errorf("convert saved root to ext4: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return SavedRootReport{}, err
	}
	if err := output.Truncate(view.length); err != nil {
		return SavedRootReport{}, err
	}
	if err := output.Sync(); err != nil {
		return SavedRootReport{}, err
	}
	return result.report, nil
}
