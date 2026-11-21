package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

func macRuntimeFailure(err error) error {
	return &failure{msg: err.Error(), try: "run scripts/mirror-mac-runtime.sh <release> with the checked guest/mac-host.lock"}
}

func loadMacRuntimePins(path string) (supply.MacRuntimeLockV1, error) {
	file, err := os.Open(path)
	if err != nil {
		return supply.MacRuntimeLockV1{}, macRuntimeFailure(err)
	}
	defer file.Close()
	lock, err := supply.ReadMacRuntimeLockV1(file)
	if err != nil {
		return lock, macRuntimeFailure(err)
	}
	return lock, nil
}

func mirrorMacRuntimeCmd(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("mirror-mac-runtime", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	release := flags.String("release", "", "the release version")
	dir := flags.String("dir", "build/mac-runtime", "the public runtime mirror folder")
	lockPath := flags.String("lock", "guest/mac-host.lock", "the checked Mac runtime pins")
	if err := flags.Parse(args); err != nil {
		return macRuntimeFailure(err)
	}
	if *release == "" || flags.NArg() != 0 {
		return macRuntimeFailure(fmt.Errorf("mirror-mac-runtime needs a release and no positional arguments"))
	}
	lock, err := loadMacRuntimePins(*lockPath)
	if err != nil {
		return err
	}
	items, err := supply.MacRuntimeManifestItemsV1(lock, *release)
	if err != nil {
		return macRuntimeFailure(err)
	}
	assets := filepath.Join(*dir, "assets")
	if err := os.MkdirAll(assets, 0755); err != nil {
		return macRuntimeFailure(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	for index, pin := range []supply.MacRuntimePinV1{lock.Lima, lock.Image} {
		path := filepath.Join(assets, pin.Asset)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := downloadMacPublic(ctx, pin, path); err != nil {
				return macRuntimeFailure(err)
			}
		} else if err != nil {
			return macRuntimeFailure(err)
		}
		if err := checkMacPublicFile(items[index], path); err != nil {
			return macRuntimeFailure(err)
		}
		fmt.Fprintf(out, "%s: %d bytes, sha256 %s\n", pin.Asset, pin.Size, pin.SHA256)
	}
	return nil
}

func downloadMacPublic(ctx context.Context, pin supply.MacRuntimePinV1, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, ResponseHeaderTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" || request.URL.User != nil || len(via) > 5 {
			return fmt.Errorf("the public Mac artefact redirected outside HTTPS")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("public Mac artefact returned HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mac-runtime-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(file, hash), response.Body, pin.Size); err != nil {
		return err
	}
	var extra [1]byte
	if n, err := response.Body.Read(extra[:]); n != 0 || err != io.EOF {
		return fmt.Errorf("public Mac artefact contains excess bytes")
	}
	if hex.EncodeToString(hash.Sum(nil)) != pin.SHA256 {
		return fmt.Errorf("public Mac artefact differs from its checked source digest")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func checkMacPublicFile(item supply.Item, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
		return fmt.Errorf("the mirrored Mac artefact has another native type or size")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return fmt.Errorf("the mirrored Mac artefact has another digest")
	}
	return nil
}

func addMacRuntimeItems(manifest *supply.Manifest, dir, lockPath string, required bool) error {
	if dir == "" {
		if required {
			return macRuntimeFailure(fmt.Errorf("-require-mac-runtime needs -mac-runtime <folder>"))
		}
		return nil
	}
	lock, err := loadMacRuntimePins(lockPath)
	if err != nil {
		return err
	}
	items, err := supply.MacRuntimeManifestItemsV1(lock, manifest.Release)
	if err != nil {
		return macRuntimeFailure(err)
	}
	for _, item := range items {
		path := filepath.Join(dir, "assets", filepath.Base(item.URL))
		if err := checkMacPublicFile(item, path); err != nil {
			return macRuntimeFailure(err)
		}
		manifest.Items = append(manifest.Items, item)
	}
	return nil
}

func checkMacRuntimeItems(manifest *supply.Manifest, required bool) error {
	has := false
	for _, item := range manifest.Items {
		has = has || item.Kind == supply.KindLima || item.Kind == supply.KindVMImage
	}
	if !required && !has {
		return nil
	}
	_, _, err := supply.MacRuntimeItemsV1(*manifest)
	if err != nil {
		return macRuntimeFailure(err)
	}
	return nil
}
