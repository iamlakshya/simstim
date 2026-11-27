package image

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/supply"
)

//go:embed default_image.toml
var defaultRecipeData string

// DefaultRecipeV1 is build input DATA. It supplies no runtime/native authority.
type DefaultRecipeV1 struct {
	Version     int                   `toml:"version" json:"v"`
	Packages    []string              `toml:"packages" json:"packages"`
	User        string                `toml:"user" json:"user"`
	UID         int                   `toml:"uid" json:"uid"`
	GID         int                   `toml:"gid" json:"gid"`
	Home        string                `toml:"home" json:"home"`
	Shell       string                `toml:"shell" json:"shell"`
	Environment []string              `toml:"environment" json:"environment"`
	Sources     []string              `toml:"sources" json:"sources"`
	Files       []DefaultRecipeFileV1 `toml:"file" json:"file"`
}

// DefaultRecipeFileV1 replaces container-injected host identity/DNS files.
type DefaultRecipeFileV1 struct {
	Path    string `toml:"path" json:"path"`
	Content string `toml:"content" json:"content"`
	Mode    int64  `toml:"mode" json:"mode"`
}

var loadDefaultRecipe = sync.OnceValues(func() (DefaultRecipeV1, error) {
	var recipe DefaultRecipeV1
	metadata, err := toml.Decode(defaultRecipeData, &recipe)
	if err != nil || len(metadata.Undecoded()) != 0 || recipe.Version != 1 || recipe.User != "agent" || recipe.UID != 1000 || recipe.GID != 1000 || recipe.Home != "/home/agent" || recipe.Shell != "/bin/bash" || len(recipe.Packages) == 0 || len(recipe.Sources) == 0 {
		return recipe, defaultBuildError("the embedded default image recipe is invalid")
	}
	seen := map[string]bool{}
	for _, name := range recipe.Packages {
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789+.-") != "" || seen[name] {
			return recipe, defaultBuildError("the default image recipe has an invalid package")
		}
		seen[name] = true
	}
	for _, source := range recipe.Sources {
		if !strings.HasPrefix(source, "https://") || strings.ContainsAny(source, " \t\r\n\x00") {
			return recipe, defaultBuildError("the default image recipe has an invalid source")
		}
	}
	paths := map[string]bool{}
	for _, file := range recipe.Files {
		if !fs.ValidPath(file.Path) || paths[file.Path] || len(file.Content) > 4096 || file.Mode < 0 || file.Mode > 0o777 {
			return recipe, defaultBuildError("the default image recipe has an invalid fixed file")
		}
		paths[file.Path] = true
	}
	return recipe, nil
})

// DefaultRecipe returns an owned copy of the one minimal build recipe.
func DefaultRecipe() (DefaultRecipeV1, error) {
	recipe, err := loadDefaultRecipe()
	recipe.Packages = slices.Clone(recipe.Packages)
	recipe.Environment = slices.Clone(recipe.Environment)
	recipe.Sources = slices.Clone(recipe.Sources)
	recipe.Files = slices.Clone(recipe.Files)
	return recipe, err
}

// DefaultRecipeSHA256 identifies the exact tracked recipe bytes.
func DefaultRecipeSHA256() string {
	sum := sha256.Sum256([]byte(defaultRecipeData))
	return hex.EncodeToString(sum[:])
}

// DefaultPackageV1 is an actual dpkg-query observation from the prepared root.
type DefaultPackageV1 struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

// DefaultInputsV1 binds the actual package-root preparation to pinned inputs.
type DefaultInputsV1 struct {
	Version         int                `json:"v"`
	Arch            string             `json:"arch"`
	BaseImage       string             `json:"base_image"`
	Snapshot        string             `json:"snapshot"`
	Suite           string             `json:"suite"`
	InReleaseSHA256 string             `json:"inrelease_sha256"`
	RecipeSHA256    string             `json:"recipe_sha256"`
	Epoch           int64              `json:"epoch"`
	Packages        []DefaultPackageV1 `json:"packages"`
}

// Validate matches actual preparation against the same locked Debian inputs.
func (input DefaultInputsV1) Validate(lock *supply.Lock) error {
	recipe, err := DefaultRecipe()
	if err != nil {
		return err
	}
	if lock == nil || lock.Validate() != nil || input.Version != 1 || input.Arch != "amd64" && input.Arch != "arm64" || input.BaseImage != lock.Builder.Image || input.Snapshot != lock.Builder.Snapshot || input.Suite != lock.Builder.Suite || input.InReleaseSHA256 != lock.Builder.InReleaseSHA256 || input.RecipeSHA256 != DefaultRecipeSHA256() || len(input.Packages) != len(recipe.Packages) {
		return defaultBuildError("default image preparation differs from its locked inputs")
	}
	epoch, err := time.Parse("20060102T150405Z", input.Snapshot)
	if err != nil || input.Epoch != epoch.Unix() {
		return defaultBuildError("default image preparation has no exact snapshot epoch")
	}
	seen := map[string]bool{}
	for _, pkg := range input.Packages {
		if !slices.Contains(recipe.Packages, pkg.Name) || seen[pkg.Name] || pkg.Version == "" || len(pkg.Version) > 256 || strings.ContainsAny(pkg.Version, " \t\r\n\x00") || pkg.Arch != input.Arch && pkg.Arch != "all" {
			return defaultBuildError("default image preparation has invalid or missing package versions")
		}
		seen[pkg.Name] = true
	}
	return nil
}

func defaultBuildError(message string) error {
	return fmt.Errorf("default image: %s. Try: prepare both architectures from the tracked recipe and locked Debian snapshot", message)
}
