package client

import (
	"fmt"
	"sync"

	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/version"
)

// releaseManifest is the existing explicit test override; production reads
// the actual current embedded/fetched helper set and verifies it below.
var releaseManifest struct {
	sync.Mutex
	m *supply.Manifest
}

type carriedFile struct {
	kind, name, arch string
	data             []byte
}

func ReleaseManifest() (*supply.Manifest, error) {
	releaseManifest.Lock()
	defer releaseManifest.Unlock()
	if releaseManifest.m != nil {
		return releaseManifest.m, nil
	}
	return loadManifest()
}

func carriedFiles() []carriedFile {
	var files []carriedFile
	for _, arch := range supply.Arches {
		for _, c := range supply.CarriedFiles {
			if b, ok := helpers.File(c.File(arch)); ok {
				files = append(files, carriedFile{c.Kind, c.Name, arch, b})
			}
		}
	}
	return files
}

func loadManifest() (*supply.Manifest, error) {
	files := carriedFiles()
	data, ok := helpers.Manifest()
	if !ok {
		m := &supply.Manifest{Release: version.Version}
		for _, f := range files {
			m.Items = append(m.Items, supply.NewItem(f.kind, f.name, version.Version, f.arch, f.data, ""))
		}
		return m, nil
	}
	m, err := supply.Load(data)
	if err != nil {
		return nil, &UserError{Msg: "this client's release manifest is damaged: " + err.Error(), Hint: "Install the client again, or rebuild it with `just build`.", Code: "manifest_damaged", Kind: KindFailed}
	}
	if err := matchCarried(m, files); err != nil {
		return nil, err
	}
	return m, nil
}

func matchCarried(m *supply.Manifest, files []carriedFile) error {
	for _, f := range files {
		var err error
		if it, ok := m.Find(f.kind, f.name, f.arch); ok {
			err = supply.Check(it, f.name, f.data)
		} else {
			err = fmt.Errorf("it names no %s for linux/%s", f.name, f.arch)
		}
		if err != nil {
			return &UserError{
				Msg:  "this client's release manifest does not match the helpers it carries: " + err.Error(),
				Hint: "Rebuild the client with `just build`, which writes both, or install a release.",
				Code: "manifest_mismatch",
				Kind: KindFailed,
			}
		}
	}
	return nil
}
