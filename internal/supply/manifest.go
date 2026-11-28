package supply

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReleaseManifest is the signed release's release-manifest.json naming every
// trusted runtime artefact by SHA-256 and exact byte size (spec 14.9.1 & 20.1).
type ReleaseManifest struct {
	Release    string     `json:"release"`
	Runtime    string     `json:"runtime"`
	Items      []Item     `json:"items"`
	Advisories []Advisory `json:"advisories,omitempty"`
}

// Item describes one trusted release asset in release-manifest.json.
type Item struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Arch       string `json:"arch,omitempty"`
	Asset      string `json:"asset,omitempty"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	URL        string `json:"url"`
	RootHash   string `json:"root_hash,omitempty"`
	Salt       string `json:"salt,omitempty"`
	DataBlocks uint64 `json:"data_blocks,omitempty"`
	Licence    string `json:"licence,omitempty"`
	EOL        string `json:"eol,omitempty"`
	HostKernel string `json:"host_kernel,omitempty"`
	SourceURL  string `json:"source_url,omitempty"`
}

// Advisory records a known-bad runtime version (spec 14.9.1 and 20.9).
type Advisory struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Before   string `json:"before"`
	Severity string `json:"severity"`
}

// KernelAssetName returns the release asset filename for arch's guest kernel
// (spec 20.1: vmlinux-amd64, Image-arm64).
func KernelAssetName(arch string) (string, error) {
	switch arch {
	case "amd64":
		return "vmlinux-amd64", nil
	case "arm64":
		return "Image-arm64", nil
	default:
		return "", fmt.Errorf("supply: unsupported kernel arch %q. Try: use amd64 or arm64", arch)
	}
}

// ConfigAssetName returns the release asset filename for arch's kernel config.
func ConfigAssetName(arch string) string { return "config-" + arch }

// ToolsAssetName returns the release asset filename for arch's tools image.
func ToolsAssetName(arch string) string { return "tools-" + arch + ".erofs" }

// KernelSourceAssetName returns the release asset filename for the kernel source tarball.
func KernelSourceAssetName(version string) string { return "linux-" + version + ".tar.xz" }

// VMMAssetName returns the mirrored VMM binary filename for arch.
func VMMAssetName(name, version, arch string) string {
	return fmt.Sprintf("%s-%s-linux-%s", name, version, arch)
}

// VMMRemoteAssetName returns the mirrored ch-remote binary filename for arch.
func VMMRemoteAssetName(version, arch string) string {
	return fmt.Sprintf("ch-remote-%s-linux-%s", version, arch)
}

// BuildManifestOptions configures BuildReleaseManifest.
type BuildManifestOptions struct {
	Lock          Lock
	Release       string
	Runtime       string
	BaseURL       string   // e.g. https://github.com/owner/repo/releases/download/v0.6.0
	AssetDir      string   // directory containing built boot pair + optional mirrored VMM assets
	Arches        []string // defaults to ["amd64", "arm64"]
	RequireVMM    bool     // fail if mirrored VMM binaries are missing from AssetDir
	RequireSource bool     // fail if mirrored kernel source tarball is missing from AssetDir
}

// BuildReleaseManifest inspects the built boot-pair assets in opts.AssetDir,
// verifies every file against opts.Lock and dm-verity, and returns a verified
// ReleaseManifest ready to write as release-manifest.json (spec 20.1).
func BuildReleaseManifest(opts BuildManifestOptions) (ReleaseManifest, error) {
	if err := CheckLock(opts.Lock); err != nil {
		return ReleaseManifest{}, err
	}
	if strings.TrimSpace(opts.Release) == "" {
		return ReleaseManifest{}, errors.New("supply: release version is required. Try: pass --release <version>")
	}
	if strings.TrimSpace(opts.Runtime) == "" {
		return ReleaseManifest{}, errors.New("supply: runtime label is required. Try: pass --runtime <label>")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if err := checkHTTPSURL("base-url", baseURL); err != nil {
		return ReleaseManifest{}, err
	}
	arches := opts.Arches
	if len(arches) == 0 {
		arches = []string{"amd64", "arm64"}
	}

	srcName := KernelSourceAssetName(opts.Lock.Kernel.Version)
	srcURL := baseURL + "/" + srcName
	if opts.RequireSource {
		if err := verifyFileMatchesPin(filepath.Join(opts.AssetDir, srcName), opts.Lock.Kernel.SourceTarball.SHA256, opts.Lock.Kernel.SourceTarball.Size); err != nil {
			return ReleaseManifest{}, err
		}
	}

	var items []Item
	for _, arch := range arches {
		kItem, err := buildKernelItem(opts.Lock.Kernel, arch, opts.AssetDir, baseURL, srcURL)
		if err != nil {
			return ReleaseManifest{}, err
		}
		items = append(items, kItem)

		tItem, err := buildToolsItem(arch, opts.AssetDir, baseURL)
		if err != nil {
			return ReleaseManifest{}, err
		}
		items = append(items, tItem)

		vmmItems, err := buildVMMItems(opts.Lock.VMMs, arch, opts.AssetDir, baseURL, opts.RequireVMM)
		if err != nil {
			return ReleaseManifest{}, err
		}
		items = append(items, vmmItems...)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].Arch < items[j].Arch
	})

	m := ReleaseManifest{
		Release: opts.Release,
		Runtime: opts.Runtime,
		Items:   items,
	}
	if err := VerifyReleaseManifest(m, opts.AssetDir); err != nil {
		return ReleaseManifest{}, err
	}
	return m, nil
}

func buildKernelItem(k KernelSpec, arch, assetDir, baseURL, srcURL string) (Item, error) {
	ka, err := k.Arch(arch)
	if err != nil {
		return Item{}, err
	}
	assetName, err := KernelAssetName(arch)
	if err != nil {
		return Item{}, err
	}
	if err := verifyFileMatchesPin(filepath.Join(assetDir, assetName), ka.SHA256, ka.Size); err != nil {
		return Item{}, err
	}
	cfgName := ConfigAssetName(arch)
	if err := verifyFileMatchesPin(filepath.Join(assetDir, cfgName), ka.ConfigSHA256, ka.ConfigSize); err != nil {
		return Item{}, err
	}
	return Item{
		Kind:      "kernel",
		Name:      "guest",
		Version:   k.Version,
		Arch:      arch,
		Asset:     assetName,
		SHA256:    ka.SHA256,
		Size:      ka.Size,
		URL:       baseURL + "/" + assetName,
		Licence:   k.Licence,
		SourceURL: srcURL,
	}, nil
}

func buildToolsItem(arch, assetDir, baseURL string) (Item, error) {
	assetName := ToolsAssetName(arch)
	metaPath := filepath.Join(assetDir, assetName+".json")
	raw, err := os.ReadFile(filepath.Clean(metaPath))
	if err != nil {
		return Item{}, fmt.Errorf("supply: read tools metadata %s: %w. Try: build tools-%s.erofs with cmd/supply build-tools first", metaPath, err, arch)
	}
	var meta VerityInfo
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Item{}, fmt.Errorf("supply: decode %s: %w", metaPath, err)
	}
	verified, err := VerifyRawVerity(filepath.Join(assetDir, assetName), meta.DataBlocks, meta.RootHash)
	if err != nil {
		return Item{}, err
	}
	return Item{
		Kind:       "tools",
		Name:       "tools",
		Arch:       arch,
		Asset:      assetName,
		SHA256:     verified.SHA256,
		Size:       verified.Size,
		URL:        baseURL + "/" + assetName,
		RootHash:   verified.RootHash,
		Salt:       verified.Salt,
		DataBlocks: verified.DataBlocks,
	}, nil
}

func buildVMMItems(vmms []VMMSpec, arch, assetDir, baseURL string, require bool) ([]Item, error) {
	var out []Item
	for _, v := range vmms {
		binPin, remotePin, err := v.BinaryFor(arch)
		if err != nil {
			return nil, err
		}
		vmmAsset := VMMAssetName(v.Name, v.Version, arch)
		vmmPath := filepath.Join(assetDir, vmmAsset)
		if _, statErr := os.Stat(vmmPath); statErr != nil {
			if require {
				return nil, fmt.Errorf("supply: missing mirrored VMM %s: %w. Try: run cmd/supply mirror-vmm first", vmmPath, statErr)
			}
			continue
		}
		if err := verifyFileMatchesPin(vmmPath, binPin.SHA256, binPin.Size); err != nil {
			return nil, err
		}
		out = append(out, Item{
			Kind:       "vmm",
			Name:       v.Name,
			Version:    v.Version,
			Arch:       arch,
			Asset:      vmmAsset,
			SHA256:     binPin.SHA256,
			Size:       binPin.Size,
			URL:        baseURL + "/" + vmmAsset,
			Licence:    v.Licence,
			EOL:        v.EOLString(),
			HostKernel: v.HostKernel,
		})

		remoteAsset := VMMRemoteAssetName(v.Version, arch)
		remotePath := filepath.Join(assetDir, remoteAsset)
		if _, statErr := os.Stat(remotePath); statErr == nil {
			if err := verifyFileMatchesPin(remotePath, remotePin.SHA256, remotePin.Size); err != nil {
				return nil, err
			}
			out = append(out, Item{
				Kind:    "vmm",
				Name:    "ch-remote",
				Version: v.Version,
				Arch:    arch,
				Asset:   remoteAsset,
				SHA256:  remotePin.SHA256,
				Size:    remotePin.Size,
				URL:     baseURL + "/" + remoteAsset,
				Licence: v.Licence,
				EOL:     v.EOLString(),
			})
		} else if require {
			return nil, fmt.Errorf("supply: missing mirrored ch-remote %s: %w. Try: run cmd/supply mirror-vmm first", remotePath, statErr)
		}
	}
	return out, nil
}

// VerifyReleaseManifest checks every asset named in m against its file on disk
// in assetDir (exact byte size, SHA-256, and raw dm-verity tree for tools
// images) before upload (spec 20.1).
func VerifyReleaseManifest(m ReleaseManifest, assetDir string) error {
	if len(m.Items) == 0 {
		return errors.New("supply: release manifest has no items. Try: rebuild the boot pair before generating release-manifest.json")
	}
	for _, it := range m.Items {
		if err := checkHTTPSURL(it.Name, it.URL); err != nil {
			return err
		}
		if !isSHA256Hex(it.SHA256) || it.Size <= 0 {
			return fmt.Errorf("supply: manifest item %s (%s) has invalid digest or size. Try: regenerate release-manifest.json", it.Name, it.Arch)
		}
		if it.Kind == "kernel" && strings.TrimSpace(it.SourceURL) == "" {
			return fmt.Errorf("supply: kernel item %s (%s) is missing source_url. Try: regenerate release-manifest.json", it.Name, it.Arch)
		}
		assetName := it.Asset
		if assetName == "" {
			assetName = filepath.Base(it.URL)
		}
		if filepath.Base(assetName) != assetName || assetName == "." || assetName == ".." {
			return fmt.Errorf("supply: invalid asset filename %q in manifest. Try: regenerate release-manifest.json", assetName)
		}
		p := filepath.Join(assetDir, assetName)
		if err := verifyFileMatchesPin(p, it.SHA256, it.Size); err != nil {
			return err
		}
		if it.Kind == "tools" {
			if it.Salt != ZeroSaltHex {
				return fmt.Errorf("supply: tools item %s has salt %q, want %s", assetName, it.Salt, ZeroSaltHex)
			}
			if _, err := VerifyRawVerity(p, it.DataBlocks, it.RootHash); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteReleaseManifest writes m atomically as indented JSON to path.
func WriteReleaseManifest(path string, m ReleaseManifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("supply: encode release manifest: %w", err)
	}
	raw = append(raw, '\n')
	clean := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(clean), 0o700); err != nil {
		return fmt.Errorf("supply: create manifest directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(clean), ".release-manifest-*.json")
	if err != nil {
		return fmt.Errorf("supply: create temp manifest: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("supply: write manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("supply: close manifest: %w", err)
	}
	if err := os.Rename(tmpPath, clean); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("supply: install manifest %s: %w", clean, err)
	}
	return nil
}

// LoadReleaseManifest reads and decodes release-manifest.json from path.
func LoadReleaseManifest(path string) (ReleaseManifest, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ReleaseManifest{}, fmt.Errorf("supply: read %s: %w. Try: pass the path to release-manifest.json", path, err)
	}
	var m ReleaseManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return ReleaseManifest{}, fmt.Errorf("supply: decode %s: %w. Try: regenerate release-manifest.json", path, err)
	}
	return m, nil
}

func verifyFileMatchesPin(path, wantSHA string, wantSize int64) error {
	gotSHA, gotSize, err := FileDigest(path)
	if err != nil {
		return fmt.Errorf("%w. Try: run guest/build.sh to produce all boot-pair assets", err)
	}
	if gotSize != wantSize {
		return fmt.Errorf("supply: %s size is %d bytes, want %d bytes. Try: download or rebuild it again", filepath.Base(path), gotSize, wantSize)
	}
	if !strings.EqualFold(gotSHA, wantSHA) {
		return fmt.Errorf("supply: %s does not match its SHA-256 in guest/supply.lock. Try: download it again. If it still differs, stop, and check upstream checksums", filepath.Base(path))
	}
	return nil
}
