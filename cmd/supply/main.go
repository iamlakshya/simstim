// Command supply writes and checks the release manifest (isolation spec
// 14.9.1 and 20.1), and verifies build inputs against guest/supply.lock.
// It is a build tool and is never shipped.
//
//	go run ./cmd/supply write -release 0.6.0 [-guest build/guest] internal/client/helpers
//	go run ./cmd/supply assets internal/client/helpers/release-manifest.json
//	go run ./cmd/supply check [-require-guest] -url <release URL> -dir <downloads> internal/client/helpers/release-manifest.json
//	go run ./cmd/supply pin [-lock guest/supply.lock] <get|verify> ...
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
)

// failure is an error with what to try next.
type failure struct {
	msg, try string
}

func (f *failure) Error() string { return f.msg }

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "supply:", err)
		var f *failure
		if errors.As(err, &f) && f.try != "" {
			fmt.Fprintln(os.Stderr, "Try:", f.try)
		}
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	usage := &failure{msg: "say write, assets, check, pin or mirror-vmm", try: "go run ./cmd/supply write -release <version> internal/client/helpers"}
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "write":
		return write(args[1:], out)
	case "assets":
		return assets(args[1:], out)
	case "check":
		return check(args[1:], out)
	case "pin":
		return pinCmd(args[1:], out)
	case "mirror-vmm":
		return mirrorVMMCmd(args[1:], out)
	}
	return usage
}

// knownBadList is the list that write puts in the manifest. Tests replace
// it.
var knownBadList = supply.KnownBad

func write(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	release := fs.String("release", "", "the release's version, such as 0.6.0")
	dest := fs.String("o", "", "where to write the manifest (default: "+supply.FileName+" in the folder)")
	guestDir := fs.String("guest", "", "the folder that holds the built guest kernel and tools image")
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	configsDir := fs.String("configs", "guest/kernel", "folder holding config-amd64 and config-arm64")
	requireGuest := fs.Bool("require-guest", false, "fail unless the guest boot pair for both architectures is present")
	vmmDir := fs.String("vmm", "", "the VMM build folder, holding inputs and assets")
	requireVMM := fs.Bool("require-vmm", false, "fail unless every pinned VMM is mirrored for both architectures")
	if err := fs.Parse(args); err != nil {
		return &failure{msg: err.Error(), try: "go run ./cmd/supply write -release <version> internal/client/helpers"}
	}
	if *release == "" || fs.NArg() != 1 {
		return &failure{msg: "write needs -release and one folder", try: "go run ./cmd/supply write -release <version> internal/client/helpers"}
	}
	dir := fs.Arg(0)
	if *dest == "" {
		*dest = filepath.Join(dir, supply.FileName)
	}
	list := knownBadList()
	if err := supply.ValidateAdvisories(list); err != nil {
		return &failure{msg: "internal/supply/advisories.go: " + err.Error(),
			try: "fix the entry in internal/supply/advisories.go, then run go test ./internal/supply"}
	}
	m := &supply.Manifest{Release: strings.TrimPrefix(*release, "v"), Advisories: list}
	folder := supply.ReleaseFolder(m.Release)
	for _, c := range supply.CarriedFiles {
		for _, arch := range supply.Arches {
			data, err := os.ReadFile(filepath.Join(dir, c.File(arch)))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return &failure{msg: err.Error(), try: "build the helpers first: just helpers"}
			}
			m.Items = append(m.Items, supply.NewItem(c.Kind, c.Name, m.Release, arch, data, folder+"/"+c.AssetName(arch)))
		}
	}
	if len(m.Items) == 0 {
		return &failure{msg: "no helper in " + dir, try: "build the helpers first: just helpers"}
	}
	if *guestDir != "" || *requireGuest {
		if err := addGuestItems(m, *guestDir, *lockPath, *configsDir, *requireGuest); err != nil {
			return err
		}
	}
	if *vmmDir != "" || *requireVMM {
		if err := addVMMItems(m, *vmmDir, *lockPath, *requireVMM); err != nil {
			return err
		}
	}
	b, err := supply.Marshal(m)
	if err != nil {
		return &failure{msg: err.Error(), try: "check -release"}
	}
	if err := os.WriteFile(*dest, b, 0o644); err != nil { //nolint:gosec // the manifest holds digests, not secrets, and the release publishes it
		return &failure{msg: err.Error(), try: "check that the folder can be written"}
	}
	fmt.Fprintf(out, "wrote %s: %d items for release %s\n", *dest, len(m.Items), m.Release)
	return nil
}

func load(p string) (*supply.Manifest, error) {
	b, err := os.ReadFile(p) //nolint:gosec // the build names the manifest to read, as it names every other file
	if err != nil {
		return nil, &failure{msg: err.Error(), try: "go run ./cmd/supply write -release <version> internal/client/helpers"}
	}
	m, err := supply.Load(b)
	if err != nil {
		return nil, &failure{msg: err.Error(), try: "write the manifest again with go run ./cmd/supply write"}
	}
	return m, nil
}

// assets prints the name of each release file that check checks, one on
// each line: every item's URL, every GPL source_url, and each kernel's config.
func assets(args []string, out io.Writer) error {
	if len(args) != 1 {
		return &failure{msg: "assets needs the manifest", try: "go run ./cmd/supply assets internal/client/helpers/" + supply.FileName}
	}
	m, err := load(args[0])
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	emit := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			fmt.Fprintln(out, name)
		}
	}
	for _, it := range m.Items {
		if it.URL != "" {
			emit(path.Base(it.URL))
		}
	}
	for _, it := range m.Items {
		if it.SourceURL != "" {
			emit(path.Base(it.SourceURL))
		}
		if it.Kind == supply.KindKernel {
			emit(supply.ConfigAsset(it.Arch))
		}
	}
	return nil
}

// check checks that the manifest is for the release that -url names, that no
// item of the release matches its own known-bad list, and that each asset
// matches the manifest and guest/supply.lock.
func check(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	base := fs.String("url", "", "the release's download URL, such as https://github.com/<owner>/<repo>/releases/download/v0.6.0")
	dir := fs.String("dir", "", "the folder that holds the release's files")
	lockPath := fs.String("lock", "guest/supply.lock", "path to guest/supply.lock")
	configsDir := fs.String("configs", "guest/kernel", "folder holding config-amd64 and config-arm64")
	requireGuest := fs.Bool("require-guest", false, "fail unless the manifest names the guest boot pair for both architectures")
	vmmDir := fs.String("vmm", "", "the VMM build folder holding the original pinned inputs")
	requireVMM := fs.Bool("require-vmm", false, "fail unless the manifest names every pinned VMM on both architectures")
	if err := fs.Parse(args); err != nil {
		return &failure{msg: err.Error(), try: "go run ./cmd/supply check -url <release URL> -dir <folder> <manifest>"}
	}
	if *base == "" || *dir == "" || fs.NArg() != 1 {
		return &failure{msg: "check needs -url, -dir and the manifest", try: "go run ./cmd/supply check -url <release URL> -dir <folder> <manifest>"}
	}
	m, err := load(fs.Arg(0))
	if err != nil {
		return err
	}
	want := strings.TrimSuffix(*base, "/")
	if got := supply.ReleaseFolder(m.Release); got != want {
		return &failure{msg: fmt.Sprintf("the manifest points at %s, not at this release, %s", got, want),
			try: "build the release from its own tag, in the project's own repository"}
	}
	if err := knownBad(m); err != nil {
		return err
	}
	if *requireGuest {
		if err := ensureBootPairPresent(m); err != nil {
			return err
		}
	}
	if err := checkVMMReleaseExtras(m, *dir, *vmmDir, *lockPath, *requireVMM, out); err != nil {
		return err
	}
	for _, it := range m.Items {
		if it.URL == "" {
			continue
		}
		name := path.Base(it.URL)
		if err := supply.CheckFile(it, filepath.Join(*dir, name)); err != nil {
			return &failure{msg: err.Error(), try: "download " + name + " from the release again. If it still differs, stop the release and find out why."}
		}
		fmt.Fprintf(out, "%s: %d bytes, sha256 ok\n", name, it.Size)
	}
	return checkKernelReleaseExtras(m, *dir, *lockPath, *configsDir, out)
}

func knownBad(m *supply.Manifest) error {
	found := m.OwnFindings()
	if len(found) == 0 {
		return nil
	}
	lines := make([]string, len(found))
	var pin, vt, unresolved bool
	for i, f := range found {
		lines[i] = "release: " + f.String() + "."
		isVT := f.Item.Kind == supply.KindVT
		pin = pin || !isVT
		vt = vt || isVT && !f.Advisory.Unresolved
		unresolved = unresolved || isVT && f.Advisory.Unresolved
	}
	var try []string
	if pin {
		try = append(try, "bump the pin in guest/supply.lock")
	}
	if vt {
		try = append(try, "move the Ghostty pin past the fix (vt/README.md)")
	}
	if unresolved {
		try = append(try, "once this release's shadow terminal carries the fixed Ghostty pin (vt/README.md), "+
			"set the entry's before to "+m.Release+" in internal/supply/advisories.go, and drop unresolved")
	}
	return &failure{msg: strings.Join(lines, "\n"), try: strings.Join(try, ", and ")}
}
