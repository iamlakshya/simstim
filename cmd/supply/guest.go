package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
)

type verityMeta struct {
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	RootHash   string `json:"root_hash"`
	Salt       string `json:"salt"`
	DataBlocks int64  `json:"data_blocks"`
}

func addGuestItems(m *supply.Manifest, guestDir, lockPath, configsDir string, requireAll bool) error {
	if guestDir == "" {
		return &failure{msg: "-require-guest needs -guest <folder>", try: "build the boot pair first: just guest"}
	}
	assetsDir := filepath.Join(guestDir, "assets")
	if _, err := os.Stat(assetsDir); errors.Is(err, os.ErrNotExist) {
		if requireAll {
			return &failure{msg: "no guest assets in " + assetsDir, try: "build the boot pair first: just guest"}
		}
		return nil
	}
	lock, err := loadLock(lockPath)
	if err != nil {
		return err
	}
	folder := supply.ReleaseFolder(m.Release)
	srcAsset := supply.KernelSourceAsset(lock.Kernel.Version)
	var addedKernel bool

	for _, arch := range supply.Arches {
		added, err := addArchGuestKernel(m, assetsDir, configsDir, folder, srcAsset, arch, lock, requireAll)
		if err != nil {
			return err
		}
		if added {
			addedKernel = true
		}
		if err := addArchGuestTools(m, guestDir, assetsDir, folder, arch, requireAll); err != nil {
			return err
		}
	}

	if addedKernel || requireAll {
		if err := verifyPinnedFile(filepath.Join(assetsDir, srcAsset), lock.Kernel.SHA256, 0, true); err != nil {
			return err
		}
	}
	return nil
}

func addArchGuestKernel(m *supply.Manifest, assetsDir, configsDir, folder, srcAsset, arch string, lock *supply.Lock, requireAll bool) (bool, error) {
	kName := supply.KernelAsset(arch)
	kPath := filepath.Join(assetsDir, kName)
	kData, err := os.ReadFile(kPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !requireAll {
			return false, nil
		}
		return false, &failure{msg: "read " + kPath + ": " + err.Error(), try: "build the boot pair first: just guest"}
	}
	it := supply.NewItem(supply.KindKernel, "guest", lock.Kernel.Version, arch, kData, folder+"/"+kName)
	it.Licence = "GPL-2.0-only"
	it.SourceURL = folder + "/" + srcAsset
	it.Released = lock.Kernel.ReleasedDate()
	m.Items = append(m.Items, it)
	if err := verifyConfigCopy(filepath.Join(assetsDir, supply.ConfigAsset(arch)), filepath.Join(configsDir, supply.ConfigAsset(arch))); err != nil {
		return false, err
	}
	return true, nil
}

func addArchGuestTools(m *supply.Manifest, guestDir, assetsDir, folder, arch string, requireAll bool) error {
	tName := supply.ToolsAsset(arch)
	tPath := filepath.Join(assetsDir, tName)
	tData, err := os.ReadFile(tPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !requireAll {
			return nil
		}
		return &failure{msg: "read " + tPath + ": " + err.Error(), try: "build the boot pair first: just guest"}
	}
	vm, err := readVerityMeta(filepath.Join(guestDir, "tools-"+arch+".verity.json"))
	if err != nil {
		return err
	}
	it := supply.NewItem(supply.KindTools, "tools", m.Release, arch, tData, folder+"/"+tName)
	if it.SHA256 != vm.SHA256 || it.Size != vm.Size {
		return &failure{msg: fmt.Sprintf("%s does not match tools-%s.verity.json", tPath, arch),
			try: "rebuild the tools image and its verity tree: just guest"}
	}
	it.RootHash = vm.RootHash
	it.Salt = vm.Salt
	it.DataBlocks = vm.DataBlocks
	if err := supply.CheckFile(it, tPath); err != nil {
		return &failure{msg: "check tools image: " + err.Error(), try: "rebuild the tools image and its verity tree: just guest"}
	}
	m.Items = append(m.Items, it)
	return nil
}

func readVerityMeta(p string) (verityMeta, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return verityMeta{}, &failure{msg: "read " + p + ": " + err.Error(), try: "run go run ./guest/verity on the tools image"}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var vm verityMeta
	if err := dec.Decode(&vm); err != nil {
		return verityMeta{}, &failure{msg: "parse " + p + ": " + err.Error(), try: "run go run ./guest/verity on the tools image again"}
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return verityMeta{}, &failure{msg: p + " holds more than one JSON object", try: "run go run ./guest/verity on the tools image again"}
	}
	return vm, nil
}

func ensureBootPairPresent(m *supply.Manifest) error {
	for _, arch := range supply.Arches {
		if _, ok := m.Find(supply.KindKernel, "guest", arch); !ok {
			return &failure{msg: fmt.Sprintf("the release manifest lacks the %s guest kernel", arch),
				try: "build the boot pair before GoReleaser runs"}
		}
		if _, ok := m.Find(supply.KindTools, "tools", arch); !ok {
			return &failure{msg: fmt.Sprintf("the release manifest lacks the %s tools image", arch),
				try: "build the boot pair before GoReleaser runs"}
		}
	}
	return nil
}

func checkKernelReleaseExtras(m *supply.Manifest, dir, lockPath, configsDir string, out io.Writer) error {
	var kernels []supply.Item
	for _, it := range m.Items {
		if it.Kind == supply.KindKernel {
			kernels = append(kernels, it)
		}
	}
	if len(kernels) == 0 {
		return nil
	}
	lock, err := loadLock(lockPath)
	if err != nil {
		return err
	}
	checkedSrc := map[string]bool{}
	for _, it := range kernels {
		if it.Version != lock.Kernel.Version || it.Released != lock.Kernel.ReleasedDate() {
			return &failure{msg: fmt.Sprintf("kernel %s in the manifest is %s (%s), and guest/supply.lock pins %s (%s)",
				it.Arch, it.Version, it.Released, lock.Kernel.Version, lock.Kernel.ReleasedDate()),
				try: "rebuild the release manifest from the current guest/supply.lock"}
		}
		srcName := path.Base(it.SourceURL)
		if !checkedSrc[srcName] {
			srcPath := filepath.Join(dir, srcName)
			if err := verifyPinnedFile(srcPath, lock.Kernel.SHA256, 0, true); err != nil {
				return err
			}
			fi, err := os.Stat(srcPath)
			if err != nil {
				return &failure{msg: err.Error(), try: "download " + srcName + " from the release again"}
			}
			fmt.Fprintf(out, "%s: %d bytes, sha256 ok\n", srcName, fi.Size())
			checkedSrc[srcName] = true
		}
		cfgName := supply.ConfigAsset(it.Arch)
		cfgPath := filepath.Join(dir, cfgName)
		if err := verifyConfigCopy(cfgPath, filepath.Join(configsDir, cfgName)); err != nil {
			return err
		}
		fi, err := os.Stat(cfgPath)
		if err != nil {
			return &failure{msg: err.Error(), try: "download " + cfgName + " from the release again"}
		}
		fmt.Fprintf(out, "%s: %d bytes, config ok\n", cfgName, fi.Size())
	}
	return nil
}

func verifyConfigCopy(gotPath, wantPath string) error {
	got, err := os.ReadFile(gotPath)
	if err != nil {
		return &failure{msg: "read " + gotPath + ": " + err.Error(),
			try: "copy " + filepath.Base(wantPath) + " into the release assets"}
	}
	want, err := os.ReadFile(wantPath)
	if err != nil {
		return &failure{msg: "read " + wantPath + ": " + err.Error(),
			try: "check that " + wantPath + " exists in the repository"}
	}
	if !bytes.Equal(got, want) {
		return &failure{msg: filepath.Base(gotPath) + " does not match " + wantPath + ".",
			try: "rebuild the guest assets from the committed config in guest/kernel/"}
	}
	return nil
}

func loadLock(lockPath string) (*supply.Lock, error) {
	lock, err := supply.LoadLockFile(lockPath)
	if err != nil {
		return nil, &failure{msg: err.Error(), try: "fix guest/supply.lock, then run go test ./internal/supply"}
	}
	return lock, nil
}

func verifyPinnedFile(filePath, wantSHA string, wantSize int64, isKernel bool) error {
	base := filepath.Base(filePath)
	hint := "Download it again. If it still differs, stop, and check the upstream release against guest/supply.lock."
	if isKernel {
		hint = "Download it again. If it still differs, stop, and check kernel.org's signed sha256sums.asc."
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		return &failure{msg: "cannot read " + filePath + ": " + err.Error(), try: hint}
	}
	if wantSize > 0 && fi.Size() != wantSize {
		return &failure{msg: base + " does not match its SHA-256 in guest/supply.lock.", try: hint}
	}
	if err := supply.CheckPinnedSHA256(filePath, wantSHA); err != nil {
		var me *supply.MismatchError
		if errors.As(err, &me) {
			return &failure{msg: base + " does not match its SHA-256 in guest/supply.lock.", try: hint}
		}
		return &failure{msg: err.Error(), try: hint}
	}
	return nil
}

func pinCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	if err := fs.Parse(args); err != nil {
		return &failure{msg: err.Error(), try: "go run ./cmd/supply pin get kernel.version"}
	}
	rest := fs.Args()
	if len(rest) < 2 {
		return &failure{msg: "pin needs get <key> or verify <target> <file>", try: "go run ./cmd/supply pin get kernel.version"}
	}
	lock, err := loadLock(*lockPath)
	if err != nil {
		return err
	}
	switch rest[0] {
	case "get":
		if len(rest) != 2 {
			return &failure{msg: "pin get needs one key", try: "go run ./cmd/supply pin get kernel.version"}
		}
		val, err := lookupLockKey(lock, rest[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(out, val)
		return nil
	case "verify":
		if len(rest) != 3 {
			return &failure{msg: "pin verify needs <target> and <file>", try: "go run ./cmd/supply pin verify kernel linux.tar.xz"}
		}
		return verifyPinTarget(lock, rest[1], rest[2])
	default:
		return &failure{msg: "unknown pin subcommand " + strconv.Quote(rest[0]), try: "use pin get or pin verify"}
	}
}

func lookupLockKey(lock *supply.Lock, key string) (string, error) {
	switch key {
	case "vmm.names":
		var names []string
		for _, v := range lock.VMMs {
			names = append(names, v.Name)
		}
		return strings.Join(names, "\n"), nil
	case "kernel.version":
		return lock.Kernel.Version, nil
	case "kernel.released":
		return lock.Kernel.ReleasedDate(), nil
	case "kernel.url":
		return lock.Kernel.URL, nil
	case "kernel.sha256":
		return lock.Kernel.SHA256, nil
	case "erofs.version":
		return lock.ErofsUtils.Version, nil
	case "erofs.url":
		return lock.ErofsUtils.URL, nil
	case "erofs.sha256":
		return lock.ErofsUtils.SHA256, nil
	case "moby.commit":
		return lock.MobyCheckConfig.Commit, nil
	case "moby.sha256":
		return lock.MobyCheckConfig.SHA256, nil
	case "builder.image":
		return lock.Builder.Image, nil
	case "builder.snapshot":
		return lock.Builder.Snapshot, nil
	case "builder.suite":
		return lock.Builder.Suite, nil
	case "builder.inrelease_sha256":
		return lock.Builder.InReleaseSHA256, nil
	}
	if rest, ok := strings.CutPrefix(key, "vmm."); ok {
		parts := strings.Split(rest, ".")
		if len(parts) >= 2 {
			for _, v := range lock.VMMs {
				if v.Name != parts[0] {
					continue
				}
				return lookupVMMField(v, parts[1:])
			}
		}
	}
	return "", &failure{msg: "unknown lock key " + strconv.Quote(key), try: "use kernel.url, erofs.url, builder.image or vmm.cloud-hypervisor.amd64.url"}
}

func lookupVMMField(v supply.LockVMM, parts []string) (string, error) {
	if len(parts) == 1 && parts[0] == "version" {
		return v.Version, nil
	}
	if len(parts) == 2 {
		var f supply.LockFile
		switch parts[0] {
		case "amd64":
			f = v.AMD64
		case "arm64":
			f = v.ARM64
		case "source":
			f = v.Source
		default:
			return "", &failure{msg: "unknown VMM target " + strconv.Quote(parts[0]), try: "use amd64, arm64 or source"}
		}
		switch parts[1] {
		case "input":
			return supply.VMMInputName(v, parts[0])
		case "url":
			return f.URL, nil
		case "sha256":
			return f.SHA256, nil
		case "size":
			return strconv.FormatInt(f.Size, 10), nil
		}
	}
	return "", &failure{msg: "unknown VMM field " + strconv.Quote(strings.Join(parts, ".")), try: "use version, amd64.url, amd64.sha256 or amd64.size"}
}

func verifyPinTarget(lock *supply.Lock, target, filePath string) error {
	switch target {
	case "kernel":
		return verifyPinnedFile(filePath, lock.Kernel.SHA256, 0, true)
	case "erofs":
		return verifyPinnedFile(filePath, lock.ErofsUtils.SHA256, 0, false)
	case "moby":
		return verifyPinnedFile(filePath, lock.MobyCheckConfig.SHA256, 0, false)
	case "builder-inrelease":
		return verifyPinnedFile(filePath, lock.Builder.InReleaseSHA256, 0, false)
	}
	if rest, ok := strings.CutPrefix(target, "vmm:"); ok {
		name, arch, ok := strings.Cut(rest, ":")
		if ok {
			for _, v := range lock.VMMs {
				if v.Name != name {
					continue
				}
				switch arch {
				case "amd64":
					return verifyPinnedFile(filePath, v.AMD64.SHA256, v.AMD64.Size, false)
				case "arm64":
					return verifyPinnedFile(filePath, v.ARM64.SHA256, v.ARM64.Size, false)
				case "source":
					return verifyPinnedFile(filePath, v.Source.SHA256, v.Source.Size, false)
				}
			}
		}
	}
	return &failure{msg: "unknown verify target " + strconv.Quote(target), try: "use kernel, erofs, moby or vmm:cloud-hypervisor:amd64"}
}
