package image

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

const defaultManifestType = "application/vnd.oci.image.manifest.v1+json"
const defaultConfigType = "application/vnd.oci.image.config.v1+json"
const defaultLayerType = "application/vnd.oci.image.layer.v1.tar"

type defaultDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *defaultPlatform  `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}
type defaultPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"architecture"`
}
type defaultManifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	MediaType     string              `json:"mediaType"`
	Config        defaultDescriptor   `json:"config"`
	Layers        []defaultDescriptor `json:"layers"`
}
type defaultIndex struct {
	SchemaVersion int                 `json:"schemaVersion"`
	MediaType     string              `json:"mediaType"`
	Manifests     []defaultDescriptor `json:"manifests"`
}
type defaultConfig struct {
	Created      string `json:"created"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		User       string   `json:"User"`
		Env        []string `json:"Env"`
		WorkingDir string   `json:"WorkingDir"`
	} `json:"config"`
	RootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

// PreparedDefaultV1 records actual local OCI and converted root bytes. Check
// repeats their content checks before supply may embed a public image pin.
type PreparedDefaultV1 struct {
	Version      int             `json:"v"`
	Inputs       DefaultInputsV1 `json:"inputs"`
	Image        Record          `json:"image"`
	Layer        Layer           `json:"layer"`
	ConfigDigest string          `json:"config_digest"`
	RootSHA256   string          `json:"root_sha256"`
}

// PrepareDefault makes a deterministic full-root OCI image, then derives its
// verity metadata with the same converter/Inspect code as ordinary imports.
// The build driver runs this in its approved offline memory-capped workload.
// It performs no registry access and invents no digest or root check value.
func PrepareDefault(ctx context.Context, input *os.File, inputs DefaultInputsV1, lock *supply.Lock, directory string) (PreparedDefaultV1, error) {
	var prepared PreparedDefaultV1
	if err := inputs.Validate(lock); err != nil {
		return prepared, err
	}
	root, err := cachefs.OpenPrivateRoot(directory, true)
	if err != nil {
		return prepared, err
	}
	defer root.Close()
	for _, name := range []string{"blobs", "blobs/sha256"} {
		parent, leaf, err := defaultParent(root, name)
		if err != nil {
			return prepared, err
		}
		err = parent.Mkdir(leaf, 0o700)
		parent.Close()
		if err != nil {
			return prepared, err
		}
	}
	epoch, err := cachefs.OpenFile(root, "epoch.tar", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return prepared, err
	}
	defer epoch.Close()
	if err := defaultEpochTar(ctx, input, epoch, time.Unix(inputs.Epoch, 0).UTC()); err != nil {
		return prepared, err
	}
	inputDigest, inputBytes, err := defaultFileDigest(ctx, epoch, MaxCompressed)
	if err != nil {
		return prepared, err
	}
	if _, err := epoch.Seek(0, 0); err != nil {
		return prepared, err
	}
	scratch, err := cachefs.OpenFile(root, "scratch", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return prepared, err
	}
	defer scratch.Close()
	canonical, err := cachefs.OpenFile(root, "layer.tar", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return prepared, err
	}
	defer canonical.Close()
	if _, err := Flatten(ctx, []Layer{{Digest: inputDigest, MediaType: defaultLayerType, Size: inputBytes}}, []io.Reader{epoch}, scratch, canonical); err != nil {
		return prepared, err
	}
	layerDigest, layerBytes, err := defaultFileDigest(ctx, canonical, MaxCompressed)
	if err != nil {
		return prepared, err
	}
	layer := Layer{Digest: layerDigest, MediaType: defaultLayerType, Size: layerBytes}
	if err := checkBlob(canonical, layer); err != nil {
		return prepared, err
	}
	output, err := cachefs.OpenFile(root, "rootfs.img", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return prepared, err
	}
	defer output.Close()
	if _, err := Convert(ctx, []Layer{layer}, []*os.File{canonical}, output); err != nil {
		return prepared, err
	}
	if err := checkBlob(canonical, layer); err != nil {
		return prepared, err
	}
	record, err := Inspect(output)
	if err != nil {
		return prepared, err
	}
	rootDigest, _, err := defaultFileDigest(ctx, output, maxOutput)
	if err != nil {
		return prepared, err
	}
	recipe, err := DefaultRecipe()
	if err != nil {
		return prepared, err
	}
	config := defaultConfig{Created: time.Unix(inputs.Epoch, 0).UTC().Format(time.RFC3339), Architecture: inputs.Arch, OS: "linux"}
	config.Config.User, config.Config.Env, config.Config.WorkingDir = "1000:1000", recipe.Environment, recipe.Home
	config.RootFS.Type, config.RootFS.DiffIDs = "layers", []string{layerDigest}
	configData, err := json.Marshal(config)
	if err != nil {
		return prepared, err
	}
	configDescriptor, err := defaultWriteBlob(root, defaultConfigType, configData)
	if err != nil {
		return prepared, err
	}
	manifest := defaultManifest{SchemaVersion: 2, MediaType: defaultManifestType, Config: configDescriptor, Layers: []defaultDescriptor{{MediaType: defaultLayerType, Digest: layerDigest, Size: layerBytes}}}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return prepared, err
	}
	manifestDescriptor, err := defaultWriteBlob(root, defaultManifestType, manifestData)
	if err != nil {
		return prepared, err
	}
	manifestDescriptor.Platform = &defaultPlatform{OS: "linux", Arch: inputs.Arch}
	manifestDescriptor.Annotations = map[string]string{"org.opencontainers.image.ref.name": inputs.Arch}
	index, err := json.Marshal(defaultIndex{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []defaultDescriptor{manifestDescriptor}})
	if err != nil {
		return prepared, err
	}
	if err := defaultWriteFile(root, "index.json", append(index, '\n')); err != nil {
		return prepared, err
	}
	if err := defaultWriteFile(root, "oci-layout", []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n")); err != nil {
		return prepared, err
	}
	if err := canonical.Sync(); err != nil {
		return prepared, err
	}
	if err := canonical.Chmod(0o444); err != nil {
		return prepared, err
	}
	blobsDir, blobLeaf, err := defaultParent(root, "blobs/sha256/"+layerDigest[7:])
	if err != nil {
		return prepared, err
	}
	renameErr := cachefs.RenameBetweenNoReplace(root, "layer.tar", blobsDir, blobLeaf)
	if renameErr == nil {
		renameErr = syncRoot(blobsDir)
	}
	if err := errors.Join(renameErr, blobsDir.Close()); err != nil {
		return prepared, err
	}
	if err := output.Chmod(0o444); err != nil {
		return prepared, err
	}
	record.Ref = brand.ImageRepository + "@" + manifestDescriptor.Digest
	record.Digest, record.PlatformDigest, record.Arch = manifestDescriptor.Digest, manifestDescriptor.Digest, inputs.Arch
	record.ConvertedAt, record.UsedAt = time.Unix(inputs.Epoch, 0).UTC(), time.Unix(inputs.Epoch, 0).UTC()
	prepared = PreparedDefaultV1{Version: 1, Inputs: inputs, Image: record, Layer: layer, ConfigDigest: configDescriptor.Digest, RootSHA256: rootDigest}
	metadata, err := json.Marshal(prepared)
	if err != nil {
		return prepared, err
	}
	if err := defaultWriteFile(root, "prepared.json", append(metadata, '\n')); err != nil {
		return prepared, err
	}
	for _, name := range []string{"epoch.tar", "scratch"} {
		if err := root.Remove(name); err != nil {
			return prepared, err
		}
	}
	return prepared, syncRoot(root)
}

func defaultEpochTar(ctx context.Context, input io.Reader, output io.Writer, epoch time.Time) error {
	recipe, err := DefaultRecipe()
	if err != nil {
		return err
	}
	reader := tar.NewReader(&flatContextReader{ctx, input})
	writer := tar.NewWriter(output)
	count := 0
	budget := MaxUncompressed
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return defaultBuildError("the prepared root tar is malformed")
		}
		count++
		if count > MaxEntries {
			return tooLarge("default image root has too many entries")
		}
		name, err := flatName(header.Name)
		if err != nil {
			return err
		}
		omit := name == ".dockerenv" || name == "out" || strings.HasPrefix(name, "out/") || name == "run/default-image-input" || strings.HasPrefix(name, "run/default-image-input/")
		for _, file := range recipe.Files {
			if name == file.Path {
				omit = true
			}
		}
		if omit {
			continue
		}
		normal := normalHeader(header, name)
		normal.ModTime = epoch
		if normal.Size < 0 || normal.Size > budget {
			return tooLarge("default image root exceeds16GiB")
		}
		budget -= normal.Size
		if err := writer.WriteHeader(normal); err != nil {
			return err
		}
		if _, err := io.CopyN(writer, &flatContextReader{ctx, reader}, normal.Size); err != nil {
			return err
		}
	}
	for _, file := range recipe.Files {
		header := &tar.Header{Name: file.Path, Mode: file.Mode, Uid: 0, Gid: 0, Size: int64(len(file.Content)), Typeflag: tar.TypeReg, ModTime: epoch, Format: tar.FormatPAX}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, file.Content); err != nil {
			return err
		}
	}
	return writer.Close()
}

func defaultFileDigest(ctx context.Context, file *os.File, maximum int64) (string, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return "", 0, defaultBuildError("an image build artifact has an invalid type or size")
	}
	hash := sha256.New()
	count, err := io.Copy(hash, &flatContextReader{ctx, io.NewSectionReader(file, 0, info.Size())})
	if err != nil {
		return "", 0, err
	}
	if count != info.Size() {
		return "", 0, defaultBuildError("an image artifact changed while hashing")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), count, nil
}

func defaultParent(root *os.Root, name string) (*os.Root, string, error) {
	store := &Store{root: root}
	return store.parent(name)
}
func defaultWriteFile(root *os.Root, name string, data []byte) error {
	parent, leaf, err := defaultParent(root, name)
	if err != nil {
		return err
	}
	defer parent.Close()
	file, err := cachefs.OpenFile(parent, leaf, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(0o444); err != nil {
		return err
	}
	return file.Sync()
}
func defaultWriteBlob(root *os.Root, kind string, data []byte) (defaultDescriptor, error) {
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	err := defaultWriteFile(root, "blobs/sha256/"+digest[7:], data)
	return defaultDescriptor{MediaType: kind, Digest: digest, Size: int64(len(data))}, err
}
