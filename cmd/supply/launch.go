package main

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/supply"
)

func addLaunchItems(manifest *supply.Manifest, directory string, required bool) error {
	for _, arch := range supply.Arches {
		name := "launch-" + arch
		file := filepath.Join(directory, name)
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) && !required {
			continue
		}
		if err != nil {
			return &failure{msg: "read actual launcher: " + err.Error(), try: "build both static launchers from this exact release source"}
		}
		image, err := elf.Open(file)
		if err != nil {
			return err
		}
		machine := elf.EM_X86_64
		if arch == "arm64" {
			machine = elf.EM_AARCH64
		}
		valid := image.Class == elf.ELFCLASS64 && image.Machine == machine
		for _, program := range image.Progs {
			if program.Type == elf.PT_INTERP {
				valid = false
			}
		}
		image.Close()
		if !valid {
			return &failure{msg: fmt.Sprintf("%s is not the actual static Linux/%s launcher", name, arch), try: "build each launcher with CGO_ENABLED=0 and its exact GOARCH"}
		}
		item := supply.NewItem(supply.KindLaunch, "launch", manifest.Release, arch, data, supply.ReleaseFolder(manifest.Release)+"/"+name)
		item.Licence = "Apache-2.0"
		manifest.Items = append(manifest.Items, item)
	}
	return nil
}

func checkLaunchItems(manifest *supply.Manifest, required bool) error {
	for _, arch := range supply.Arches {
		item, found := manifest.Find(supply.KindLaunch, "launch", arch)
		if !found {
			if required {
				return &failure{msg: "release manifest lacks an actual launcher for " + arch, try: "build both launchers before writing the release manifest"}
			}
			continue
		}
		if item.Version != manifest.Release || item.URL != supply.ReleaseFolder(manifest.Release)+"/launch-"+arch || item.Licence != "Apache-2.0" {
			return &failure{msg: "launcher release metadata differs from its original artifact contract", try: "write the manifest from this release's actual launcher binaries"}
		}
	}
	return nil
}
