package supply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// VMMInputName names the upstream download. Firecracker's architecture pins
// cover the release archives; the manifest covers the extracted executables.
func VMMInputName(v LockVMM, target string) (string, error) {
	name, err := VMMAssetName(v, target)
	if err == nil && v.Name == FirecrackerName && target != "source" {
		name += ".tgz"
	}
	return name, err
}

// MirrorVMMSource checks and mirrors the pinned source archive. It returns no
// release item: the architecture items refer to this same release asset.
func MirrorVMMSource(v LockVMM, input, outputDir string) error {
	name, err := VMMAssetName(v, "source")
	if err != nil {
		return err
	}
	data, err := checkedVMMInput(v.Source, input)
	if err != nil {
		return err
	}
	return writeVMMAsset(outputDir, name, bytes.NewReader(data), 0644)
}

// MirrorVMM checks the upstream pin and the already mirrored source. For
// Firecracker it extracts only the named runtime executable, never the jailer.
// Its returned manifest item hashes the actual raw file in outputDir.
func MirrorVMM(release string, v LockVMM, arch, input, outputDir string) (Item, error) {
	name, err := VMMAssetName(v, arch)
	if err != nil {
		return Item{}, err
	}
	if arch != "amd64" && arch != "arm64" {
		return Item{}, fmt.Errorf("VMM architecture %q must be amd64 or arm64", arch)
	}
	source, _ := VMMAssetName(v, "source")
	_, err = checkedVMMInput(v.Source, filepath.Join(outputDir, source))
	if err != nil {
		return Item{}, fmt.Errorf("check mirrored VMM source: %w", err)
	}
	data, err := checkedVMMInput(vmmPin(v, arch), input)
	if err != nil {
		return Item{}, err
	}
	if v.Name == FirecrackerName {
		runtime, err := firecrackerExecutable(v, arch, bytes.NewReader(data))
		if err != nil {
			return Item{}, err
		}
		data, err = io.ReadAll(runtime)
		if err != nil {
			return Item{}, err
		}
	}
	if err := writeVMMAsset(outputDir, name, bytes.NewReader(data), 0755); err != nil {
		return Item{}, err
	}
	out := filepath.Join(outputDir, name)
	h := sha256.Sum256(data)
	item, err := releaseVMMItem(release, v, arch, LockFile{SHA256: hex.EncodeToString(h[:]), Size: int64(len(data))})
	if err != nil {
		return Item{}, err
	}
	return item, CheckFile(item, out)
}

// Read once and check those same bytes. A changed download cannot pass its
// digest check and then supply different bytes to the archive reader.
func checkedVMMInput(pin LockFile, input string) ([]byte, error) {
	if pin.Size <= 0 || pin.Size > 256<<20 || !IsHex64(pin.SHA256) {
		return nil, fmt.Errorf("VMM input needs a SHA-256 and size between 1 and 256 MiB")
	}
	f, err := openRegular(input)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, pin.Size+1))
	if err != nil {
		return nil, err
	}
	if err := checkBytes(filepath.Base(input), pin.Size, pin.SHA256, int64(len(data)), bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return data, nil
}

func writeVMMAsset(dir, name string, r io.Reader, mode os.FileMode) error {
	f, err := os.CreateTemp(dir, ".vmm-")
	if err != nil {
		return fmt.Errorf("create mirrored VMM: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write mirrored VMM: %w", err)
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}
