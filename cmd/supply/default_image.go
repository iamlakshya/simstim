package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

func defaultImageCmd(args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "repro" {
		return defaultImageReproCmd(args[1:], out)
	}
	if len(args) == 0 {
		return &failure{msg: "default-image needs config, inputs, prepare, check or repository", try: "go run ./cmd/supply default-image config"}
	}
	if args[0] == "repository" && len(args) == 1 {
		fmt.Fprintln(out, brand.ImageRepository)
		return nil
	}
	if args[0] == "config" && len(args) <= 2 {
		recipe, err := image.DefaultRecipe()
		if err != nil {
			return err
		}
		if len(args) == 2 {
			switch args[1] {
			case "packages":
				for _, name := range recipe.Packages {
					fmt.Fprintln(out, name)
				}
			case "user":
				fmt.Fprintln(out, recipe.User)
			case "uid":
				fmt.Fprintln(out, recipe.UID)
			case "gid":
				fmt.Fprintln(out, recipe.GID)
			case "home":
				fmt.Fprintln(out, recipe.Home)
			case "shell":
				fmt.Fprintln(out, recipe.Shell)
			case "sha256":
				fmt.Fprintln(out, image.DefaultRecipeSHA256())
			default:
				return &failure{msg: "unknown default recipe field", try: "read config without a field to see the checked recipe"}
			}
			return nil
		}
		return json.NewEncoder(out).Encode(recipe)
	}
	fs := flag.NewFlagSet("default-image "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	arch := fs.String("arch", "", "linux image architecture")
	lockPath := fs.String("lock", "guest/supply.lock", "locked Debian build inputs")
	inputPath := fs.String("inputs", "", "observed preparation input JSON")
	packagesPath := fs.String("packages", "", "actual dpkg-query package/version/architecture TSV")
	tarPath := fs.String("tar", "", "actual exported prepared root tar")
	directory := fs.String("dir", "", "task-owned image output directory")
	dest := fs.String("o", "", "where to write observed input JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return &failure{msg: err.Error(), try: "use the tracked guest/base-image/build.sh producer"}
	}
	if fs.NArg() != 0 {
		return &failure{msg: "default-image takes only its named flags", try: "use the tracked guest/base-image/build.sh producer"}
	}
	lock, err := loadLock(*lockPath)
	if err != nil {
		return err
	}
	switch args[0] {
	case "inputs":
		if *packagesPath == "" || *dest == "" {
			return &failure{msg: "inputs needs -packages and -o", try: "use the actual locked-root producer"}
		}
		data, err := os.ReadFile(*packagesPath)
		if err != nil {
			return err
		}
		stamp, err := time.Parse("20060102T150405Z", lock.Builder.Snapshot)
		if err != nil {
			return err
		}
		inputs := image.DefaultInputsV1{Version: 1, Arch: *arch, BaseImage: lock.Builder.Image, Snapshot: lock.Builder.Snapshot, Suite: lock.Builder.Suite, InReleaseSHA256: lock.Builder.InReleaseSHA256, RecipeSHA256: image.DefaultRecipeSHA256(), Epoch: stamp.Unix()}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) != 3 {
				return &failure{msg: "dpkg-query did not supply exact package/version/architecture rows", try: "run the tracked root preparation, not an invented metadata file"}
			}
			inputs.Packages = append(inputs.Packages, image.DefaultPackageV1{Name: fields[0], Version: fields[1], Arch: fields[2]})
		}
		if err := inputs.Validate(lock); err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(inputs, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(*dest, append(encoded, '\n'), 0o600)
	case "prepare":
		if *inputPath == "" || *tarPath == "" || *directory == "" {
			return &failure{msg: "prepare needs -inputs, -tar and -dir", try: "use the tracked locked-root producer"}
		}
		var inputs image.DefaultInputsV1
		if err := defaultInputJSON(*inputPath, &inputs); err != nil {
			return err
		}
		if *arch != "" && inputs.Arch != *arch {
			return &failure{msg: "the observed root architecture differs from -arch", try: "build each architecture in its matching approved workload"}
		}
		file, err := os.Open(*tarPath)
		if err != nil {
			return err
		}
		defer file.Close()
		prepared, err := image.PrepareDefault(context.Background(), file, inputs, lock, *directory)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(prepared)
	case "check":
		if *directory == "" {
			return &failure{msg: "check needs -dir", try: "name the actual prepared per-architecture output"}
		}
		checked, err := image.CheckDefault(context.Background(), *directory, lock)
		if err != nil {
			return err
		}
		item, err := checked.SupplyItem()
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(item)
	case "published":
		if *directory == "" {
			return &failure{msg: "published needs -dir", try: "name the actual checked per-architecture output"}
		}
		checked, err := image.CheckDefault(context.Background(), *directory, lock)
		if err != nil {
			return err
		}
		item, err := checked.SupplyItem()
		if err != nil {
			return err
		}
		ref, err := image.ParseReference(brand.ImageRepository + "@" + item.Digest)
		if err != nil {
			return err
		}
		registry := &image.Registry{}
		document, err := registry.Manifest(context.Background(), ref, item.Digest)
		if err != nil {
			return &failure{msg: "the actual default manifest is not anonymously readable by its pinned digest: " + err.Error(), try: "publish the exact OCI artifact in the tag workflow and make the default package readable before releasing"}
		}
		platform, err := image.Platform(document, item.Arch)
		if err != nil || platform != item.Digest {
			return &failure{msg: "the published default differs from its actual per-architecture pin", try: "preserve OCI digests during publication and verify the exact architecture"}
		}
		return json.NewEncoder(out).Encode(item)
	case "epoch":
		fmt.Fprintln(out, strconv.FormatInt(mustDefaultEpoch(lock), 10))
		return nil
	default:
		return &failure{msg: "unknown default-image operation", try: "say config, inputs, prepare, check or repository"}
	}
}

func mustDefaultEpoch(lock *supply.Lock) int64 {
	stamp, _ := time.Parse("20060102T150405Z", lock.Builder.Snapshot)
	return stamp.Unix()
}
func defaultInputJSON(name string, target any) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, image.MaxManifest+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > image.MaxManifest || !api.ExactJSONFields(data, target) || json.Unmarshal(data, target) != nil {
		return &failure{msg: "default image preparation input is malformed or too large", try: "use the actual locked root preparation output"}
	}
	return nil
}

func addDefaultImageItems(manifest *supply.Manifest, directory, lockPath string, required bool) error {
	if directory == "" {
		return &failure{msg: "-require-images needs -images <folder>", try: "prepare both default image architectures first"}
	}
	lock, err := loadLock(lockPath)
	if err != nil {
		return err
	}
	for _, arch := range supply.Arches {
		path := filepath.Join(directory, arch)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && !required {
			continue
		}
		checked, err := image.CheckDefault(context.Background(), path, lock)
		if err != nil {
			return err
		}
		item, err := checked.SupplyItem()
		if err != nil {
			return err
		}
		if item.Arch != arch {
			return &failure{msg: "a prepared default image has the wrong architecture", try: "prepare amd64 and arm64 independently"}
		}
		manifest.Items = append(manifest.Items, item)
	}
	return nil
}

func checkDefaultImageItems(manifest *supply.Manifest, directory, lockPath string, required bool) error {
	var actual supply.Manifest
	if err := addDefaultImageItems(&actual, directory, lockPath, required); err != nil {
		return err
	}
	for _, item := range actual.Items {
		declared, found := manifest.Find(supply.KindImage, "agent-base", item.Arch)
		if !found || !reflect.DeepEqual(declared, item) {
			return &failure{msg: "release default image pin differs from actual OCI/conversion artifacts", try: "write the manifest from the same checked prepared image output"}
		}
	}
	if required && len(actual.Items) != len(supply.Arches) {
		return &failure{msg: "release lacks both actual default image architectures", try: "prepare amd64 and arm64 before embedding"}
	}
	return nil
}
