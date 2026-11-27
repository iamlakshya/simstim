//go:build darwin

package join

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

func (e *macExecutor) runtimeDir() string {
	return filepath.Join(filepath.Dir(macHelperPath(e.ledger.Account)), "runtime")
}
func (e *macExecutor) limaBinary() string {
	return filepath.Join(e.runtimeDir(), "lima", "bin", "limactl")
}
func (e *macExecutor) imagePath() string { return filepath.Join(e.runtimeDir(), "base.img") }

func (e *macExecutor) downloadMacItem(ctx context.Context, item supply.Item, name string) (*os.File, error) {
	cache := filepath.Join(e.dir, "downloads")
	if err := macRootDir(cache, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(cache, name)
	if file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err == nil {
		if err := checkMacItem(ctx, item, file); err != nil {
			file.Close()
			return nil, err
		}
		return file, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := e.intent("download-"+name, ""); err != nil {
		return nil, err
	}
	limited, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(limited, http.MethodGet, item.URL, nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, ResponseHeaderTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) > 5 || request.URL.Scheme != "https" || request.URL.User != nil {
			return macFailure("join_mac_download", "the pinned public download redirected outside HTTPS")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength >= 0 && response.ContentLength != item.Size {
		return nil, macFailure("join_mac_download", "the pinned public artefact has another status or size")
	}
	file, err := os.CreateTemp(cache, ".download-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	ok := false
	defer func() {
		if !ok {
			file.Close()
		}
	}()
	if _, err := io.CopyN(file, response.Body, item.Size); err != nil {
		return nil, err
	}
	var extra [1]byte
	if n, err := response.Body.Read(extra[:]); n != 0 || err != io.EOF {
		return nil, macFailure("join_mac_download", "the pinned public artefact contains extra bytes")
	}
	if err := errors.Join(file.Chmod(0444), file.Sync()); err != nil {
		return nil, err
	}
	if err := checkMacItem(ctx, item, file); err != nil {
		return nil, err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return nil, err
	}
	if err := macSyncDir(cache); err != nil {
		return nil, err
	}
	if err := e.applied("download-" + name); err != nil {
		return nil, err
	}
	ok = true
	return file, nil
}

func checkMacItem(ctx context.Context, item supply.Item, file *os.File) error {
	if item.Size <= 0 || item.Size > 8<<30 || !supply.IsHex64(item.SHA256) {
		return macFailure("join_mac_runtime", "the pinned Mac item has invalid byte bounds")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.Size {
		return macFailure("join_mac_runtime", "the retained Mac item has another native type or size")
	}
	hash := sha256.New()
	reader := io.NewSectionReader(file, 0, item.Size)
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return macFailure("digest_mismatch", "the Mac artefact differs from this original signed release")
	}
	return nil
}

func (e *macExecutor) installMacRuntime(ctx context.Context) error {
	if step := e.step("runtime"); step != nil && step.State == "applied" {
		actual, err := macTreeIdentity(e.runtimeDir())
		if err != nil || actual != step.After {
			return macFailure("join_mac_runtime", "the original installed Mac runtime changed")
		}
		return nil
	}
	if _, err := e.intent("runtime", e.runtimeDir()); err != nil {
		return err
	}
	if _, err := os.Lstat(e.runtimeDir()); err == nil {
		step := e.step("runtime")
		actual, readErr := macTreeIdentity(e.runtimeDir())
		if readErr == nil && step.After.Exists && actual == step.After {
			return e.applied("runtime")
		}
		return macFailure("join_mac_runtime", "an uncommitted runtime directory must be retained and inspected before retry")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	archive, err := e.downloadMacItem(ctx, e.plan.lima, "lima.tar.gz")
	if err != nil {
		return err
	}
	defer archive.Close()
	image, err := e.downloadMacItem(ctx, e.plan.image, "base.img")
	if err != nil {
		return err
	}
	defer image.Close()
	temporary, err := os.MkdirTemp(filepath.Dir(e.runtimeDir()), ".runtime-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0755); err != nil {
		return err
	}
	limaDir := filepath.Join(temporary, "lima")
	if err := os.Mkdir(limaDir, 0755); err != nil {
		return err
	}
	if err := extractMacLima(archive, limaDir); err != nil {
		return err
	}
	imageTarget := filepath.Join(temporary, "base.img")
	if err := os.Link(filepath.Join(e.dir, "downloads", "base.img"), imageTarget); err != nil {
		return err
	}
	if err := os.Chmod(imageTarget, 0444); err != nil {
		return err
	}
	identity, err := macTreeIdentity(temporary)
	if err != nil {
		return err
	}
	e.step("runtime").After = identity
	if err := e.save(); err != nil {
		return err
	}
	if err := os.Rename(temporary, e.runtimeDir()); err != nil {
		return err
	}
	if err := macSyncDir(filepath.Dir(e.runtimeDir())); err != nil {
		return err
	}
	return e.applied("runtime")
}

func extractMacLima(archive *os.File, root string) error {
	info, err := archive.Stat()
	if err != nil {
		return err
	}
	reader, err := gzip.NewReader(io.NewSectionReader(archive, 0, info.Size()))
	if err != nil {
		return err
	}
	defer reader.Close()
	tarReader := tar.NewReader(io.LimitReader(reader, 512<<20+1))
	count := 0
	var total int64
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		count++
		if count > 4096 {
			return macFailure("join_mac_archive", "the pinned Lima archive has too many objects")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == "." || name == "" {
			continue
		}
		if strings.HasPrefix(name, "/") || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\x00\r\n") {
			return macFailure("join_mac_archive", "the Lima archive has an unsafe member name")
		}
		if header.Typeflag == tar.TypeSymlink && name == "bin/lima" && header.Linkname == "limactl" {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 128<<20 || total > 512<<20-header.Size {
			return macFailure("join_mac_archive", "the pinned Lima archive contains an unsupported object or size")
		}
		total += header.Size
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, tarReader, header.Size)
		mode := os.FileMode(0444)
		if header.Mode&0111 != 0 {
			mode = 0555
		}
		if err := errors.Join(copyErr, file.Chmod(mode), file.Sync(), file.Close()); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(filepath.Join(root, "bin", "limactl"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	binary, err := file.Stat()
	if err != nil || !binary.Mode().IsRegular() || binary.Mode().Perm() != 0555 {
		return macFailure("join_mac_archive", "the pinned archive has no immutable Lima executable")
	}
	return macSyncDir(root)
}

func macTreeIdentity(root string) (macFileState, error) {
	info, err := macProtected(root, true)
	if err != nil {
		return macFileState{}, err
	}
	hash := sha256.New()
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(paths) >= 4096 {
			return macFailure("join_mac_runtime", "the runtime has too many native objects")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return macFailure("join_mac_runtime", "the runtime contains a symbolic link")
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return macFileState{}, err
	}
	sort.Strings(paths)
	for _, path := range paths {
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return macFileState{}, err
		}
		native, ok := fileInfo.Sys().(*syscall.Stat_t)
		if !ok || native.Uid != 0 || fileInfo.Mode().Perm()&0022 != 0 {
			return macFileState{}, macFailure("join_mac_runtime", "the root-owned runtime contains another writer")
		}
		relative, _ := filepath.Rel(root, path)
		fmt.Fprintf(hash, "%s:%o:%d\n", relative, fileInfo.Mode().Perm(), fileInfo.Size())
		if fileInfo.Mode().IsRegular() {
			file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return macFileState{}, err
			}
			_, err = io.Copy(hash, file)
			err = errors.Join(err, file.Close())
			if err != nil {
				return macFileState{}, err
			}
		} else if !fileInfo.IsDir() {
			return macFileState{}, macFailure("join_mac_runtime", "the runtime contains another native object type")
		}
	}
	native := info.Sys().(*syscall.Stat_t)
	return macFileState{Exists: true, Device: uint64(native.Dev), Inode: native.Ino, Mode: uint32(info.Mode().Perm()), SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
