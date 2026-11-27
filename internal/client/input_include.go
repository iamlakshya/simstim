package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
)

// addReviewedIncludes is called only with H5's original reviewed LOCAL
// primary include selection. A public InputFile.Include value never reaches
// this constructor or grants a source read.
func (p *copyPlan) addReviewedIncludes(ctx context.Context, primary discoveredInputSeed, selected []string, limit int64) error {
	root, err := os.OpenRoot(primary.root)
	if err != nil {
		return err
	}
	defer root.Close()
	identity, err := root.Stat(".")
	if err != nil || !os.SameFile(identity, primary.identity) {
		return copyInputError("include_scope", "the reviewed primary source changed")
	}
	for _, name := range slices.Compact(slices.Sorted(slices.Values(selected))) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(p.files) >= api.CopyFileMax {
			return copyInputError("copy_too_many", "laptop copies and reviewed includes exceed 10000 transferred leaves")
		}
		if !files.Relative(name) {
			return &UserError{Code: "bad_include", Msg: "a reviewed include must stay beneath the primary repository",
				Hint: "Use a relative regular file such as --include .env.", Kind: KindUsage}
		}
		file, err := files.OpenIncluded(root, name)
		if err != nil {
			return copyInputError("bad_include", "the reviewed include leaves the primary source or enters Git metadata")
		}
		info, err := file.Stat()
		if err != nil || info.Size() < 0 || info.Size() > limit-p.bytes {
			file.Close()
			return copyInputError("copy_too_big", "laptop copies and reviewed includes exceed copy_max")
		}
		sum := sha256.New()
		n, err := io.Copy(sum, &inputContextReader{ctx: ctx, reader: io.LimitReader(file, info.Size()+1)})
		file.Close()
		if err != nil {
			return err
		}
		if n != info.Size() {
			return copyInputError("copy_changed", "the reviewed include changed while sealing")
		}
		guest := path.Join("/work", primary.name, name)
		if !api.InputGuestPath(guest) {
			return copyInputError("bad_include", "the reviewed include has an unsafe guest destination")
		}
		p.files = append(p.files, plannedCopy{root: copyRoot{name: primary.root, info: primary.identity}, name: name, info: info, include: true,
			file: api.InputFile{Path: guest, Size: n, Mode: uint32(info.Mode().Perm()), SHA256: hex.EncodeToString(sum.Sum(nil)), Include: true}})
		p.bytes += n
	}
	return nil
}
