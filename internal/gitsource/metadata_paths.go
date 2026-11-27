package gitsource

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/files"
)

// seedRoot discovers Git metadata without running host Git in the source.
func (a sourceAuthority) seedRoot(dir string) (root string, metadata []string, err error) {
	selected, err := a.pinTrustedSource(dir, false)
	if err != nil {
		return "", nil, err
	}
	defer selected.close()
	dir = selected.name

	for top := dir; ; top = filepath.Dir(top) {
		r, e := os.OpenRoot(top)
		if e != nil {
			return "", nil, e
		}
		st, e := r.Lstat(".git")
		if errors.Is(e, os.ErrNotExist) {
			bare := bareMetadata(r)
			r.Close()
			if bare {
				if err := a.trustedSource(top); err != nil {
					return "", nil, err
				}
				metadata, err := a.seedMetadata(top, "", []string{top})
				return top, metadata, err
			}
			if top == filepath.Dir(top) {
				return "", nil, nil
			}
			continue
		}
		if e != nil {
			r.Close()
			return "", nil, e
		}
		gitdir := filepath.Join(top, ".git")
		if st.Mode().IsRegular() {
			f, e := files.OpenRegular(r, ".git")
			if e != nil {
				r.Close()
				return "", nil, e
			}
			if e = a.checkMetadataIdentity(f); e != nil {
				f.Close()
				r.Close()
				return "", nil, e
			}
			b, e := io.ReadAll(io.LimitReader(f, 4097))
			f.Close()
			if e != nil || len(b) > 4096 {
				r.Close()
				return "", nil, metadataRefused()
			}
			line, parseErr := metadataLine(b)
			if parseErr != nil {
				r.Close()
				return "", nil, parseErr
			}
			pointer, ok := strings.CutPrefix(line, "gitdir: ")
			if !ok || pointer == "" || strings.TrimSpace(pointer) != pointer {
				r.Close()
				return "", nil, metadataRefused()
			}
			if !filepath.IsAbs(pointer) {
				pointer = top + string(filepath.Separator) + pointer
			}
			gitdir = pointer
		} else if st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			metadataRoot, e := a.pinTrustedSource(gitdir, true)
			if e != nil {
				r.Close()
				return "", nil, e
			}
			gitdir = metadataRoot.name
			metadataRoot.close()
		} else {
			r.Close()
			return "", nil, metadataRefused()
		}
		r.Close()
		canonicalGit, e := a.pinTrustedSource(gitdir, true)
		if e != nil {
			return "", nil, e
		}
		gitdir = canonicalGit.name
		canonicalGit.close()
		p, e := a.pinMetadataDirectory(gitdir)
		if e != nil {
			return "", nil, e
		}
		p.Close()
		metadata = append(metadata, gitdir)
		g, e := os.OpenRoot(gitdir)
		if e != nil {
			return "", nil, e
		}
		f, e := files.OpenContainedRegular(g, "commondir")
		if e == nil {
			if e = a.checkMetadataIdentity(f); e != nil {
				f.Close()
				g.Close()
				return "", nil, e
			}
			b, readErr := io.ReadAll(io.LimitReader(f, 4097))
			f.Close()
			if readErr != nil || len(b) > 4096 {
				g.Close()
				return "", nil, metadataRefused()
			}
			common, parseErr := metadataLine(b)
			if parseErr != nil {
				g.Close()
				return "", nil, parseErr
			}
			if !filepath.IsAbs(common) {
				common = gitdir + string(filepath.Separator) + common
			}
			canonical, e := a.pinTrustedSource(common, true)
			if e != nil {
				g.Close()
				return "", nil, e
			}
			common = canonical.name
			canonical.close()
			if err = a.trustedSource(common); err != nil {
				g.Close()
				return "", nil, err
			}
			p, e := a.pinMetadataDirectory(common)
			if e != nil {
				g.Close()
				return "", nil, e
			}
			p.Close()
			metadata = append(metadata, common)
		} else if !errors.Is(e, os.ErrNotExist) {
			g.Close()
			return "", nil, metadataRefused()
		}
		g.Close()
		if err = a.trustedSource(top); err != nil {
			return "", nil, err
		}
		view, err := a.seedMetadata(top, filepath.Join(top, ".git"), metadata)
		return top, view, err
	}
}
