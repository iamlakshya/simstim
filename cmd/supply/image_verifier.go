package main

import (
	"debug/elf"
	"debug/macho"
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/supply"
)

func addImageVerifierItems(manifest *supply.Manifest, directory string, required bool) error {
	for _, system := range []string{"darwin", "linux"} {
		for _, arch := range supply.Arches {
			name := "image-verifier-" + system + "-" + arch
			file := filepath.Join(directory, name)
			data, err := os.ReadFile(file)
			if errors.Is(err, os.ErrNotExist) && !required {
				continue
			}
			if err != nil {
				return &failure{msg: "read actual offline image verifier: " + err.Error(), try: "run just image-verifiers to build all four offline verifier binaries"}
			}
			if !validImageVerifierFile(file, system, arch) {
				return &failure{msg: "offline verifier binary has another format or architecture", try: "build the isolated verifier module for its exact client OS/architecture"}
			}
			item := supply.NewItem(supply.KindImageVerifier, "image-verifier-"+system, manifest.Release, arch, data, supply.ReleaseFolder(manifest.Release)+"/"+name)
			item.Licence = "Apache-2.0"
			manifest.Items = append(manifest.Items, item)
		}
	}
	return nil
}

func validImageVerifierFile(name, system, arch string) bool {
	if system == "linux" {
		file, err := elf.Open(name)
		if err != nil {
			return false
		}
		defer file.Close()
		machine := elf.EM_X86_64
		if arch == "arm64" {
			machine = elf.EM_AARCH64
		}
		if file.Class != elf.ELFCLASS64 || file.Machine != machine {
			return false
		}
		for _, program := range file.Progs {
			if program.Type == elf.PT_INTERP {
				return false
			}
		}
		return true
	}
	file, err := macho.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	cpu := macho.CpuAmd64
	if arch == "arm64" {
		cpu = macho.CpuArm64
	}
	return file.Type == macho.TypeExec && file.Cpu == cpu
}

func checkImageVerifierItems(manifest *supply.Manifest, required bool) error {
	for _, system := range []string{"darwin", "linux"} {
		for _, arch := range supply.Arches {
			item, found := manifest.Find(supply.KindImageVerifier, "image-verifier-"+system, arch)
			if !found {
				if required {
					return &failure{msg: "release lacks the exact offline verifier for " + system + "/" + arch, try: "prepare all four isolated verifier artifacts before embedding"}
				}
				continue
			}
			if item.Version != manifest.Release || item.URL != supply.ReleaseFolder(manifest.Release)+"/image-verifier-"+system+"-"+arch || item.Licence != "Apache-2.0" {
				return &failure{msg: "offline verifier metadata changed its exact executable role", try: "write the manifest from the actual isolated helper artifacts"}
			}
		}
	}
	return nil
}
