package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/supply"
)

func mirrorVMMCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mirror-vmm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	release := fs.String("release", "", "the release version")
	dir := fs.String("dir", "build/vmm", "the VMM build folder holding inputs and assets")
	lockPath := fs.String("lock", "guest/supply.lock", "the supply lock")
	if err := fs.Parse(args); err != nil {
		return vmmFailure(err)
	}
	if *release == "" || fs.NArg() != 0 {
		return vmmFailure(fmt.Errorf("mirror-vmm needs -release and no positional arguments"))
	}
	m := &supply.Manifest{Release: *release}
	if err := addVMMItems(m, *dir, *lockPath, true); err != nil {
		return err
	}
	for _, it := range m.Items {
		fmt.Fprintf(out, "%s %s: %d raw bytes, sha256 %s\n", it.Name, it.Arch, it.Size, it.SHA256)
	}
	return nil
}

func vmmFailure(err error) error {
	return &failure{msg: err.Error(), try: "run scripts/mirror-vmm.sh <release>, using the current guest/supply.lock"}
}

func addVMMItems(m *supply.Manifest, dir, lockPath string, required bool) error {
	if dir == "" {
		return vmmFailure(fmt.Errorf("-require-vmm needs -vmm <folder>"))
	}
	if _, err := os.Stat(filepath.Join(dir, "inputs")); err != nil {
		if os.IsNotExist(err) && !required {
			return nil
		}
		return vmmFailure(err)
	}
	lock, err := loadLock(lockPath)
	if err != nil {
		return err
	}
	output := filepath.Join(dir, "assets")
	if err := os.MkdirAll(output, 0755); err != nil {
		return vmmFailure(err)
	}
	for _, v := range lock.VMMs {
		source, err := supply.VMMInputName(v, "source")
		if err != nil {
			return vmmFailure(err)
		}
		if err := supply.MirrorVMMSource(v, filepath.Join(dir, "inputs", source), output); err != nil {
			return vmmFailure(err)
		}
		for _, arch := range supply.Arches {
			input, err := supply.VMMInputName(v, arch)
			if err != nil {
				return vmmFailure(err)
			}
			it, err := supply.MirrorVMM(m.Release, v, arch, filepath.Join(dir, "inputs", input), output)
			if err != nil {
				return vmmFailure(err)
			}
			m.Items = append(m.Items, it)
		}
	}
	label, err := runtimeLabel(m)
	if err != nil {
		return vmmFailure(err)
	}
	m.Runtime = label
	return nil
}

// The runtime label comes from the client's actual release entries. Helper
// files do not affect it, and no host inventory contributes authority.
func runtimeLabel(m *supply.Manifest) (string, error) {
	items := []supply.Item{}
	for _, it := range m.Items {
		switch it.Kind {
		case supply.KindVMM, supply.KindKernel, supply.KindTools, supply.KindGvisor, supply.KindUserns:
			items = append(items, it)
		}
	}
	b, err := supply.Marshal(&supply.Manifest{Release: m.Release, Items: items})
	if err != nil {
		return "", err
	}
	label := supply.NewItem(supply.KindVMM, "label", m.Release, "amd64", b, "")
	return "runtime-" + label.SHA256[:32], nil
}

func checkVMMReleaseExtras(m *supply.Manifest, downloads, buildDir, lockPath string, required bool, out io.Writer) error {
	var hasVMM bool
	for _, it := range m.Items {
		hasVMM = hasVMM || it.Kind == supply.KindVMM
	}
	if !hasVMM && !required {
		return nil
	}
	lock, err := loadLock(lockPath)
	if err != nil {
		return err
	}
	expected := &supply.Manifest{Release: m.Release}
	if buildDir == "" {
		return vmmFailure(fmt.Errorf("checking VMM release files needs -vmm <build folder> with the original pinned inputs"))
	}
	if err := addVMMItems(expected, buildDir, lockPath, true); err != nil {
		return err
	}
	label, err := runtimeLabel(m)
	if err != nil || m.Runtime != label {
		return vmmFailure(fmt.Errorf("the release manifest's runtime label differs from its actual runtime entries"))
	}
	for _, want := range expected.Items {
		got, ok := m.Find(want.Kind, want.Name, want.Arch)
		if !ok || got != want {
			return vmmFailure(fmt.Errorf("the release manifest's %s for %s differs from its verified pinned input", want.Name, want.Arch))
		}
	}
	for _, it := range m.Items {
		if it.Kind == supply.KindVMM {
			if _, ok := expected.Find(it.Kind, it.Name, it.Arch); !ok {
				return vmmFailure(fmt.Errorf("the release manifest names unpinned VMM %s", it.Name))
			}
		}
	}
	for _, v := range lock.VMMs {
		name, _ := supply.VMMAssetName(v, "source")
		if err := verifyPinnedFile(filepath.Join(downloads, name), v.Source.SHA256, v.Source.Size, false); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d bytes, source sha256 ok\n", name, v.Source.Size)
	}
	return nil
}
