package image

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

type agentTarEntry struct {
	name     string
	typeflag byte
	mode     int64
	size     int64
	modTime  time.Time
	linkname string
	root     *os.Root // non-nil for regular files read via os.Root
	relPath  string   // path inside root
}

type agentsPlan struct {
	entries map[string]agentTarEntry
	roots   []*os.Root
	bytes   int64
	maxB    int64
	maxE    int
}

func (p *agentsPlan) close() {
	for _, r := range p.roots {
		_ = r.Close()
	}
}

func (p *agentsPlan) add(e agentTarEntry, kind string) error {
	if _, exists := p.entries[e.name]; exists {
		return nil
	}
	if len(p.entries)+1 > p.maxE {
		return agentsDiskError(fmt.Sprintf("%s's install folders hold more than %d files", kind, p.maxE), "", nil)
	}
	if e.size > p.maxB-p.bytes {
		return agentsDiskError(fmt.Sprintf("%s's install folders exceed %d bytes", kind, p.maxB), "", nil)
	}
	p.entries[e.name] = e
	p.bytes += e.size
	return nil
}

func (p *agentsPlan) ensureParents(tarPath, kind string) error {
	dir := path.Dir(tarPath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	cur := ""
	for _, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur += "/" + part
		}
		if err := p.add(agentTarEntry{
			name:     cur,
			typeflag: tar.TypeDir,
			mode:     0o755,
		}, kind); err != nil {
			return err
		}
	}
	return nil
}

func collectAgentsPlan(sources []AgentSource, maxBytes int64, maxEntries int) (*agentsPlan, []string, error) {
	p := &agentsPlan{
		entries: make(map[string]agentTarEntry),
		maxB:    maxBytes,
		maxE:    maxEntries,
	}
	var kinds []string
	seenKinds := make(map[string]bool)

	sorted := append([]AgentSource(nil), sources...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].BinaryName < sorted[j].BinaryName
	})

	for _, src := range sorted {
		if err := p.addSource(src); err != nil {
			p.close()
			return nil, nil, err
		}
		if !seenKinds[src.Kind] {
			seenKinds[src.Kind] = true
			kinds = append(kinds, src.Kind)
		}
	}
	return p, kinds, nil
}

func (p *agentsPlan) addSource(src AgentSource) error {
	if src.BinaryPath == "" {
		return agentsDiskError(
			src.Kind+" is in the agent table, but its binary is missing",
			fmt.Sprintf("%s agents install %s <host>", brand.Name, src.Kind),
			nil,
		)
	}
	resolved, err := filepath.EvalSymlinks(src.BinaryPath)
	if err != nil {
		return agentsDiskError(
			fmt.Sprintf("%s is in the agent table, but its binary %s is missing", src.Kind, src.BinaryPath),
			fmt.Sprintf("%s agents install %s <host>", brand.Name, src.Kind),
			err,
		)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return agentsDiskError(fmt.Sprintf("resolve %s binary: %v", src.Kind, err), "", err)
	}

	for _, dir := range src.InstallDirs {
		if dir == "" {
			continue
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		st, err := os.Stat(absDir)
		if err != nil || !st.IsDir() {
			continue
		}
		if err := p.collectDir(absDir, src.Kind); err != nil {
			return err
		}
	}

	binDir := filepath.Dir(resolved)
	binBase := filepath.Base(resolved)
	r, err := os.OpenRoot(binDir)
	if err != nil {
		return agentsDiskError(fmt.Sprintf("open %s binary folder: %v", src.Kind, err), "", err)
	}
	p.roots = append(p.roots, r)
	fi, err := r.Lstat(binBase)
	if err != nil || !fi.Mode().IsRegular() {
		return agentsDiskError(
			fmt.Sprintf("%s binary %s is not a regular file", src.Kind, resolved),
			fmt.Sprintf("%s agents install %s <host>", brand.Name, src.Kind),
			err,
		)
	}
	binTarPath := hostToRootTarPath(resolved)
	if err := p.ensureParents(binTarPath, src.Kind); err != nil {
		return err
	}
	if err := p.add(agentTarEntry{
		name:     binTarPath,
		typeflag: tar.TypeReg,
		mode:     int64(fi.Mode().Perm()),
		size:     fi.Size(),
		modTime:  fi.ModTime().UTC(),
		root:     r,
		relPath:  binBase,
	}, src.Kind); err != nil {
		return err
	}

	binName := src.BinaryName
	if binName == "" {
		binName = filepath.Base(src.BinaryPath)
	}
	linkPath := "bin/" + binName
	if err := p.ensureParents(linkPath, src.Kind); err != nil {
		return err
	}
	return p.add(agentTarEntry{
		name:     linkPath,
		typeflag: tar.TypeSymlink,
		mode:     0o777,
		linkname: "../" + binTarPath,
	}, src.Kind)
}

func (p *agentsPlan) collectDir(absDir, kind string) error {
	r, err := os.OpenRoot(absDir)
	if err != nil {
		return agentsDiskError(fmt.Sprintf("open %s folder %s: %v", kind, absDir, err), "", err)
	}
	p.roots = append(p.roots, r)

	baseTar := hostToRootTarPath(absDir)
	if err := p.ensureParents(baseTar, kind); err != nil {
		return err
	}
	if fi, err := os.Stat(absDir); err == nil {
		if err := p.add(agentTarEntry{
			name:     baseTar,
			typeflag: tar.TypeDir,
			mode:     int64(fi.Mode().Perm()),
			modTime:  fi.ModTime().UTC(),
		}, kind); err != nil {
			return err
		}
	}
	return p.walkRoot(r, ".", baseTar, kind)
}

func (p *agentsPlan) walkRoot(r *os.Root, rel, tarPrefix, kind string) error {
	f, err := r.Open(rel)
	if err != nil {
		return agentsDiskError(fmt.Sprintf("read %s folder %s: %v", kind, rel, err), "", err)
	}
	entries, rerr := f.ReadDir(-1)
	_ = f.Close()
	if rerr != nil {
		return agentsDiskError(fmt.Sprintf("list %s folder %s: %v", kind, rel, rerr), "", rerr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, de := range entries {
		childRel := de.Name()
		if rel != "." {
			childRel = rel + "/" + de.Name()
		}
		childTar := tarPrefix + "/" + de.Name()
		fi, err := r.Lstat(childRel)
		if err != nil {
			return agentsDiskError(fmt.Sprintf("stat %s file %s: %v", kind, childRel, err), "", err)
		}
		mode := fi.Mode()
		switch {
		case mode.IsDir():
			if err := p.add(agentTarEntry{
				name:     childTar,
				typeflag: tar.TypeDir,
				mode:     int64(mode.Perm()),
				modTime:  fi.ModTime().UTC(),
			}, kind); err != nil {
				return err
			}
			if err := p.walkRoot(r, childRel, childTar, kind); err != nil {
				return err
			}
		case mode&fs.ModeSymlink != 0:
			target, err := r.Readlink(childRel)
			if err != nil {
				return agentsDiskError(fmt.Sprintf("readlink %s %s: %v", kind, childRel, err), "", err)
			}
			if err := p.add(agentTarEntry{
				name:     childTar,
				typeflag: tar.TypeSymlink,
				mode:     0o777,
				modTime:  fi.ModTime().UTC(),
				linkname: target,
			}, kind); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := p.add(agentTarEntry{
				name:     childTar,
				typeflag: tar.TypeReg,
				mode:     int64(mode.Perm()),
				size:     fi.Size(),
				modTime:  fi.ModTime().UTC(),
				root:     r,
				relPath:  childRel,
			}, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeAgentsTar(w io.Writer, plan *agentsPlan) error {
	keys := make([]string, 0, len(plan.entries))
	for k := range plan.entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return compareTarPaths(keys[i], keys[j]) })

	tw := tar.NewWriter(w)
	for _, k := range keys {
		e := plan.entries[k]
		hdr := &tar.Header{
			Typeflag: e.typeflag,
			Name:     e.name,
			Linkname: e.linkname,
			Size:     e.size,
			Mode:     e.mode,
			Uid:      agentsUID,
			Gid:      agentsGID,
			ModTime:  e.modTime,
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write agents tar header %q: %w", e.name, err)
		}
		if e.typeflag == tar.TypeReg {
			f, err := e.root.Open(e.relPath)
			if err != nil {
				return agentsDiskError(fmt.Sprintf("open %s: %v", e.name, err), "", err)
			}
			_, cerr := io.CopyN(tw, f, e.size)
			_ = f.Close()
			if cerr != nil {
				return fmt.Errorf("copy %s into agents tar: %w", e.name, cerr)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish agents tar: %w", err)
	}
	return nil
}

func hostToRootTarPath(absPath string) string {
	cleaned := filepath.ToSlash(filepath.Clean(absPath))
	cleaned = strings.TrimLeft(cleaned, "/")
	if cleaned == "" {
		return "root"
	}
	return "root/" + cleaned
}

// compareTarPaths orders every folder before its children, and siblings in byte
// order of their path components (spec 20.4).
func compareTarPaths(a, b string) bool {
	pa := strings.Split(a, "/")
	pb := strings.Split(b, "/")
	n := min(len(pa), len(pb))
	for i := range n {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}
