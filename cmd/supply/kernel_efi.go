package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/supply"
)

func addArchGuestKernelEFIV1(manifest *supply.Manifest, assets, configs, folder, source, arch string, lock *supply.Lock) (bool, error) {
	name := supply.KernelEFIAsset(arch)
	data, err := os.ReadFile(filepath.Join(assets, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, &failure{msg: err.Error(), try: "build both EFI kernel assets with the pinned guest builder"}
	}
	if err := supply.CheckKernelEFI(arch, data); err != nil {
		return false, &failure{msg: err.Error(), try: "build amd64 bzImage and arm64 raw Image with EFI_STUB enabled"}
	}
	item := supply.NewItem(supply.KindKernel, supply.KernelEFIName, lock.Kernel.Version, arch, data, folder+"/"+name)
	item.Licence, item.SourceURL, item.Released = "GPL-2.0-only", folder+"/"+source, lock.Kernel.ReleasedDate()
	item.FSVerity, err = supply.FSVerityDigest(context.Background(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false, err
	}
	if err := verifyConfigCopy(filepath.Join(assets, supply.ConfigAsset(arch)), filepath.Join(configs, supply.ConfigAsset(arch))); err != nil {
		return false, err
	}
	manifest.Items = append(manifest.Items, item)
	return true, nil
}

func ensureKernelEFIPresentV1(manifest *supply.Manifest) error {
	for _, arch := range supply.Arches {
		item, found := manifest.Find(supply.KindKernel, supply.KernelEFIName, arch)
		if !found || item.FSVerity == "" {
			return &failure{msg: fmt.Sprintf("the release manifest lacks the complete signed %s EFI kernel", arch), try: "build both EFI kernel assets before writing the manifest"}
		}
	}
	return nil
}
