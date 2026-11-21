package image

import (
	"context"
	"os"
	"reflect"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// DefaultReproV1 reports checks of TWO actual independently prepared builds.
// It is evidence only; it grants no release pin, path or publication authority.
type DefaultReproV1 struct {
	Version          int    `json:"v"`
	Arch             string `json:"arch"`
	ImageDigest      string `json:"image_digest"`
	ConfigDigest     string `json:"config_digest"`
	Layer            Layer  `json:"layer"`
	RootHash         string `json:"root_hash"`
	DataBlocks       int64  `json:"data_blocks"`
	Size             int64  `json:"size"`
	FirstRootSHA256  string `json:"first_root_sha256"`
	SecondRootSHA256 string `json:"second_root_sha256"`
}

// CompareDefaultBuilds checks both actual artifact trees before comparing
// their fixed inputs, OCI content and converter-derived verity values.
// Whole root SHA values are reported separately: the dm-verity superblock's
// identifier is not an OCI input, so this does not claim byte-identical roots.
func CompareDefaultBuilds(ctx context.Context, firstDirectory, secondDirectory string, lock *supply.Lock) (DefaultReproV1, error) {
	var result DefaultReproV1
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := distinctDefaultBuilds(firstDirectory, secondDirectory); err != nil {
		return result, err
	}
	first, err := checkDefault(ctx, firstDirectory, lock)
	if err != nil {
		return result, err
	}
	second, err := checkDefault(ctx, secondDirectory, lock)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(first.Inputs, second.Inputs) || first.Image.Digest != second.Image.Digest || first.ConfigDigest != second.ConfigDigest || first.Layer != second.Layer || first.Image.RootHash != second.Image.RootHash || first.Image.DataBlocks != second.Image.DataBlocks || first.Image.Size != second.Image.Size {
		return result, defaultBuildError("the two checked default builds differ in their inputs, OCI content or verity values")
	}
	return DefaultReproV1{Version: 1, Arch: first.Inputs.Arch, ImageDigest: first.Image.Digest, ConfigDigest: first.ConfigDigest, Layer: first.Layer, RootHash: first.Image.RootHash, DataBlocks: first.Image.DataBlocks, Size: first.Image.Size, FirstRootSHA256: first.RootSHA256, SecondRootSHA256: second.RootSHA256}, ctx.Err()
}

func distinctDefaultBuilds(first, second string) error {
	a, err := cachefs.OpenPrivateRoot(first, false)
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := cachefs.OpenPrivateRoot(second, false)
	if err != nil {
		return err
	}
	defer b.Close()
	af, err := a.Open(".")
	if err != nil {
		return err
	}
	defer af.Close()
	bf, err := b.Open(".")
	if err != nil {
		return err
	}
	defer bf.Close()
	ai, err := af.Stat()
	if err != nil {
		return err
	}
	bi, err := bf.Stat()
	if err != nil {
		return err
	}
	if os.SameFile(ai, bi) {
		return defaultBuildError("reproducibility needs two distinct actual build directories")
	}
	return nil
}
