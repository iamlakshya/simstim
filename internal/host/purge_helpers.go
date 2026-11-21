package host

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
)

func (owner *purgeOwnerV1) removeOwnedHelpers() error {
	if owner.phaseDone("helpers") {
		return nil
	}
	if err := owner.replayResource(owner.ctx); err != nil {
		return err
	}
	var ledger installLedgerV1
	if err := owner.gate(func(control *placementControl, guard *os.Root) error {
		var err error
		ledger, err = readInstallLedgerV1(owner.ctx, guard, control)
		return err
	}); err != nil {
		return err
	}
	versions := make([]string, 0, len(ledger.Versions))
	for version := range ledger.Versions {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	// Keep the running code until all carried native helpers have been checked
	// and removed. Unlinking the final executable leaves this invocation alive.
	for _, helperPass := range []bool{false, true} {
		for _, version := range versions {
			record := ledger.Versions[version]
			path := filepath.Join(paths.BinDir(), version)
			root, err := cachefs.OpenPrivateRoot(path, false)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if err := record.Directory.matchesRoot(root); err != nil {
				_ = root.Close()
				return err
			}
			names := make([]string, 0, len(record.Files))
			for name := range record.Files {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				file := record.Files[name]
				if (file.Item.Kind == supply.KindHelper) != helperPass {
					continue
				}
				leaf := name
				if _, err := root.Lstat(leaf); errors.Is(err, os.ErrNotExist) && file.Prepared != "" {
					leaf = file.Prepared
				}
				validate := func(open *os.File) error {
					if file.Item.Kind == supply.KindHelper && filepath.Join(path, name) == owner.placement.executable {
						info, err := open.Stat()
						if err != nil || !os.SameFile(owner.placement.executableRef, info) {
							return placementError("the running helper changed its original mapped-file identity")
						}
					}
					return supply.CheckOpenContext(owner.ctx, supply.Item{Kind: file.Item.Kind, Name: file.Item.Name, Arch: runtime.GOARCH, SHA256: file.Item.SHA256, Size: file.Item.Size}, open)
				}
				if err := owner.removeKnownFile("data", filepath.Join("bin", version, leaf), file.Ref, validate); err != nil {
					_ = root.Close()
					return err
				}
			}
			if err := root.Close(); err != nil {
				return err
			}
		}
	}
	if ledger.Current != nil {
		root, err := cachefs.OpenPrivateRoot(paths.BinDir(), false)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			err = owner.removeInstalledLink(root, *ledger.Current)
			closeErr := root.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
	}
	for _, version := range versions {
		record := ledger.Versions[version]
		if record.Created {
			if err := owner.removeEmptyDirectory(filepath.Join(paths.BinDir(), version), record.Directory); err != nil {
				if owner.ctx.Err() != nil {
					return errors.Join(owner.ctx.Err(), err)
				}
				owner.reportItem(false, "helper-directory", version, err.Error())
			}
		}
	}
	var directories map[string]purgeRef
	if err := owner.gate(func(control *placementControl, _ *os.Root) error { directories = control.Refs; return nil }); err != nil {
		return err
	}
	if ref, ok := directories["directory:data/bin"]; ok {
		if err := owner.removeEmptyDirectory(paths.BinDir(), ref); err != nil {
			owner.reportItem(false, "helper-directory", "bin/", err.Error())
		}
	}
	if _, err := os.Lstat(owner.placement.executable); errors.Is(err, os.ErrNotExist) {
		owner.report.HelperGone = true
	}
	if !owner.report.HelperGone {
		owner.reportItem(false, "helper", brand.HostBinary, "the running helper has no original removable installation receipt")
	}
	if err := owner.removeRecordedRoot("data"); err != nil {
		return err
	}
	if err := owner.removeRecordedRuntimeRootsV1(); err != nil {
		return err
	}
	if err := owner.removeCreatedParents(); err != nil {
		return err
	}
	return owner.completePhase("helpers")
}

func (owner *purgeOwnerV1) removeInstalledLink(root *os.Root, original installLinkV1) error {
	capture := ".purge-current-" + owner.args.Nonce
	leaf := "current"
	if _, err := root.Lstat(leaf); errors.Is(err, os.ErrNotExist) {
		leaf = capture
	}
	if _, err := root.Lstat(leaf); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := checkInstallLinkV1(root, leaf, original); err != nil {
		return err
	}
	if leaf != capture {
		if err := cachefs.RenameNoReplace(root, leaf, capture); err != nil {
			return err
		}
	}
	if err := checkInstallLinkV1(root, capture, original); err != nil {
		return err
	}
	if err := owner.ctx.Err(); err != nil {
		return err
	}
	if err := root.Remove(capture); err != nil {
		return err
	}
	owner.reportItem(true, "helper-link", "bin/current", "")
	return syncCapturedDirectory(root)
}
