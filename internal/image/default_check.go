package image

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/supply"
)

// CheckedDefaultV1 is issued only after actual artifact validation. Raw JSON
// metadata cannot construct its release-item projection.
type CheckedDefaultV1 struct{ prepared PreparedDefaultV1 }

// CheckDefault independently checks actual prepared OCI, input and root bytes.
func CheckDefault(ctx context.Context, directory string, lock *supply.Lock) (*CheckedDefaultV1, error) {
	prepared, err := checkDefault(ctx, directory, lock)
	if err != nil {
		return nil, err
	}
	return &CheckedDefaultV1{prepared: prepared}, nil
}

func checkDefault(ctx context.Context, directory string, lock *supply.Lock) (PreparedDefaultV1, error) {
	var prepared PreparedDefaultV1
	root, err := cachefs.OpenPrivateRoot(directory, false)
	if err != nil {
		return prepared, err
	}
	defer root.Close()
	if err := defaultReadJSON(root, "prepared.json", &prepared); err != nil {
		return prepared, err
	}
	if prepared.Version != 1 {
		return prepared, defaultBuildError("prepared image metadata has an unsupported version")
	}
	if err := prepared.Inputs.Validate(lock); err != nil {
		return prepared, err
	}
	if prepared.Image.SavedHost != nil || len(prepared.Image.SavedAliases) != 0 || prepared.Image.Arch != prepared.Inputs.Arch || prepared.Image.Digest != prepared.Image.PlatformDigest || prepared.Image.Ref != brand.ImageRepository+"@"+prepared.Image.Digest || prepared.Layer.MediaType != defaultLayerType || prepared.Layer.Size <= 0 || prepared.Layer.Size > MaxCompressed || !ValidDigest(prepared.ConfigDigest) || !ValidDigest(prepared.RootSHA256) {
		return prepared, defaultBuildError("prepared default image has inconsistent provenance or descriptors")
	}
	request := Request{Ref: prepared.Image.Ref, Digest: prepared.Image.Digest, PlatformDigest: prepared.Image.PlatformDigest, Arch: prepared.Image.Arch, RootHash: prepared.Image.RootHash, DataBlocks: prepared.Image.DataBlocks}
	if err := CheckRecord(request, prepared.Image); err != nil {
		return prepared, err
	}
	var layout struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := defaultReadJSON(root, "oci-layout", &layout); err != nil {
		return prepared, err
	}
	if layout.Version != "1.0.0" {
		return prepared, defaultBuildError("the prepared OCI layout version is unsupported")
	}
	var index defaultIndex
	if err := defaultReadJSON(root, "index.json", &index); err != nil {
		return prepared, err
	}
	if index.SchemaVersion != 2 || index.MediaType != "application/vnd.oci.image.index.v1+json" || len(index.Manifests) != 1 {
		return prepared, defaultBuildError("the prepared index must name exactly one platform manifest")
	}
	entry := index.Manifests[0]
	if entry.MediaType != defaultManifestType || entry.Digest != prepared.Image.Digest || entry.Platform == nil || entry.Platform.OS != "linux" || entry.Platform.Arch != prepared.Inputs.Arch || len(entry.Annotations) != 1 || entry.Annotations["org.opencontainers.image.ref.name"] != prepared.Inputs.Arch {
		return prepared, defaultBuildError("the prepared index differs from the actual selected architecture")
	}
	var manifest defaultManifest
	if err := defaultReadBlobJSON(ctx, root, entry, &manifest); err != nil {
		return prepared, err
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != defaultManifestType || manifest.Config.MediaType != defaultConfigType || manifest.Config.Digest != prepared.ConfigDigest || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != prepared.Layer.MediaType || manifest.Layers[0].Digest != prepared.Layer.Digest || manifest.Layers[0].Size != prepared.Layer.Size {
		return prepared, defaultBuildError("the prepared manifest changed its actual config or layer")
	}
	var config defaultConfig
	if err := defaultReadBlobJSON(ctx, root, manifest.Config, &config); err != nil {
		return prepared, err
	}
	recipe, err := DefaultRecipe()
	if err != nil {
		return prepared, err
	}
	if config.OS != "linux" || config.Architecture != prepared.Inputs.Arch || config.Created != time.Unix(prepared.Inputs.Epoch, 0).UTC().Format(time.RFC3339) || config.Config.User != "1000:1000" || config.Config.WorkingDir != recipe.Home || !reflect.DeepEqual(config.Config.Env, recipe.Environment) || config.RootFS.Type != "layers" || !reflect.DeepEqual(config.RootFS.DiffIDs, []string{prepared.Layer.Digest}) {
		return prepared, defaultBuildError("the prepared config changed the fixed guest account, epoch or root layer")
	}
	layer, err := files.OpenRegular(root, "blobs/sha256/"+prepared.Layer.Digest[7:])
	if err != nil {
		return prepared, err
	}
	defer layer.Close()
	if err := checkBlob(layer, prepared.Layer); err != nil {
		return prepared, err
	}
	converted, err := files.OpenRegular(root, "rootfs.img")
	if err != nil {
		return prepared, err
	}
	defer converted.Close()
	actual, err := Inspect(converted)
	if err != nil {
		return prepared, err
	}
	digest, _, err := defaultFileDigest(ctx, converted, maxOutput)
	if err != nil {
		return prepared, err
	}
	if actual.RootHash != prepared.Image.RootHash || actual.DataBlocks != prepared.Image.DataBlocks || actual.Size != prepared.Image.Size || digest != prepared.RootSHA256 {
		return prepared, defaultBuildError("the converted default root bytes differ from their prepared checks")
	}
	return prepared, nil
}

// SupplyItem projects only this actual checked build's release fields.
func (checked *CheckedDefaultV1) SupplyItem() (supply.Item, error) {
	if checked == nil || checked.prepared.Version != 1 {
		return supply.Item{}, defaultBuildError("there is no checked default artifact")
	}
	prepared := checked.prepared
	return supply.Item{Kind: supply.KindImage, Name: "agent-base", Arch: prepared.Image.Arch, Digest: prepared.Image.Digest, RootHash: prepared.Image.RootHash, DataBlocks: prepared.Image.DataBlocks}, nil
}

// Metadata returns owned checked data for build reporting.
func (checked *CheckedDefaultV1) Metadata() (PreparedDefaultV1, error) {
	if checked == nil || checked.prepared.Version != 1 {
		return PreparedDefaultV1{}, defaultBuildError("there is no checked default artifact")
	}
	copy := checked.prepared
	copy.Inputs.Packages = append([]DefaultPackageV1(nil), copy.Inputs.Packages...)
	return copy, nil
}

func defaultReadJSON(root *os.Root, name string, target any) error {
	file, err := files.OpenRegular(root, name)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxManifest+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > MaxManifest || !api.ExactJSONFields(data, target) || json.Unmarshal(data, target) != nil {
		return defaultBuildError("a prepared default image JSON artifact is malformed or exceeds its bound")
	}
	return nil
}

func defaultReadBlobJSON(ctx context.Context, root *os.Root, descriptor defaultDescriptor, target any) error {
	if !ValidDigest(descriptor.Digest) || descriptor.Size <= 0 || descriptor.Size > MaxManifest {
		return defaultBuildError("a default OCI document descriptor is invalid")
	}
	name := "blobs/sha256/" + descriptor.Digest[7:]
	file, err := files.OpenRegular(root, name)
	if err != nil {
		return err
	}
	defer file.Close()
	digest, size, err := defaultFileDigest(ctx, file, MaxManifest)
	if err != nil {
		return err
	}
	if digest != descriptor.Digest || size != descriptor.Size {
		return defaultBuildError("the default OCI document differs from its digest or size")
	}
	return defaultReadJSON(root, name, target)
}
