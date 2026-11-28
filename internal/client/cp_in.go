package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/pkg/sftp"
)

type localEntry struct {
	rootRel   string
	remoteRel string
	isDir     bool
	size      int64
}

// CopyToSFTP copies localSrc (a file or directory) from the laptop into
// remoteDst via sc. It reads strictly through os.Root, skips symlinks/special
// files, refuses sensitive laptop paths (paste guard rules), and enforces caps.
func CopyToSFTP(sc *sftp.Client, localSrc, remoteDst string, opt CopyOptions) (CopyResult, error) {
	opt = opt.withDefaults()
	root, abs, topName, st, err := openLocalSource(localSrc)
	if err != nil {
		return CopyResult{}, err
	}
	defer root.Close()

	entries, skipped, err := collectLocalEntries(root, filepath.Dir(abs), topName, localSrc, st, opt)
	if err != nil {
		return CopyResult{}, err
	}
	dstBase, err := resolveRemotePath(sc, remoteDst)
	if err != nil {
		return CopyResult{}, err
	}
	if dstSt, err := sc.Stat(dstBase); err == nil && dstSt.IsDir() {
		dstBase = path.Join(dstBase, topName)
	}
	if !opt.Force {
		if err := checkRemoteTargetsMissing(sc, dstBase, entries); err != nil {
			return CopyResult{}, err
		}
	}
	copiedFiles, copiedBytes, err := uploadLocalEntries(sc, root, dstBase, st.IsDir(), entries, opt)
	if err != nil {
		return CopyResult{}, err
	}
	return CopyResult{Files: copiedFiles, Bytes: copiedBytes, Skipped: skipped}, nil
}

func openLocalSource(localSrc string) (*os.Root, string, string, fs.FileInfo, error) {
	if why := CheckSensitiveLocalPath(localSrc); why != "" {
		return nil, "", "", nil, &UserError{
			Msg:  fmt.Sprintf("refused to copy %s: %s", CleanPath(localSrc), why),
			Hint: "Pass credentials via secrets instead of copying private files.",
			Code: "copy_sensitive",
			Kind: KindRefused,
		}
	}
	abs, err := filepath.Abs(localSrc)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("resolve %s: %w", localSrc, err)
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return nil, "", "", nil, &UserError{
			Msg:  fmt.Sprintf("cannot find %s: %v", CleanPath(localSrc), err),
			Hint: "Check the local file path and try again.",
			Code: "cp_not_found",
			Kind: KindNotFound,
		}
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return nil, "", "", nil, &UserError{
			Msg:  CleanPath(localSrc) + " is a symbolic link; cp copies regular files and folders only",
			Hint: "Copy the target file directly.",
			Code: "cp_path",
			Kind: KindRefused,
		}
	}
	root, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("open %s: %w", filepath.Dir(abs), err)
	}
	return root, abs, filepath.Base(abs), st, nil
}

func collectLocalEntries(root *os.Root, baseDir, topName, localSrc string, st fs.FileInfo, opt CopyOptions) ([]localEntry, []string, error) {
	if !st.IsDir() {
		if !st.Mode().IsRegular() {
			return nil, nil, &UserError{
				Msg:  CleanPath(localSrc) + " is not a regular file or folder",
				Hint: "Copy a regular file or folder.",
				Code: "cp_path",
				Kind: KindRefused,
			}
		}
		if st.Size() > opt.MaxBytes {
			return nil, nil, copyTooBigError(st.Size(), opt.MaxBytes)
		}
		return []localEntry{{rootRel: topName, remoteRel: "", isDir: false, size: st.Size()}}, nil, nil
	}
	var entries []localEntry
	var skipped []string
	var totalBytes int64
	if err := walkLocalDir(root, baseDir, topName, "", opt, &entries, &skipped, &totalBytes); err != nil {
		return nil, nil, err
	}
	return entries, skipped, nil
}

func walkLocalDir(root *os.Root, baseDir, rootRel, remoteRel string, opt CopyOptions, entries *[]localEntry, skipped *[]string, totalBytes *int64) error {
	list, err := fs.ReadDir(root.FS(), rootRel)
	if err != nil {
		return fmt.Errorf("list %s: %w", rootRel, err)
	}
	for _, d := range list {
		childRoot := rootRel + "/" + d.Name()
		childRemote := d.Name()
		if remoteRel != "" {
			childRemote = remoteRel + "/" + d.Name()
		}
		fullLocal := filepath.Join(baseDir, filepath.FromSlash(childRoot))
		if why := CheckSensitiveLocalPath(fullLocal); why != "" {
			return &UserError{
				Msg:  fmt.Sprintf("refused to copy %s: %s", CleanPath(fullLocal), why),
				Hint: "Remove private files before copying the folder.",
				Code: "copy_sensitive",
				Kind: KindRefused,
			}
		}
		fi, err := root.Lstat(childRoot)
		if err != nil {
			return fmt.Errorf("stat %s: %w", childRoot, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			*skipped = append(*skipped, childRemote+" (symbolic link)")
			continue
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			*skipped = append(*skipped, childRemote+" (special file)")
			continue
		}
		if len(*entries)+1 > opt.MaxEntries {
			return &UserError{
				Msg:  fmt.Sprintf("the copy holds more than %d entries", opt.MaxEntries),
				Hint: "Copy a smaller folder.",
				Code: "cp_too_big",
				Kind: KindRefused,
			}
		}
		if fi.IsDir() {
			*entries = append(*entries, localEntry{rootRel: childRoot, remoteRel: childRemote, isDir: true})
			if err := walkLocalDir(root, baseDir, childRoot, childRemote, opt, entries, skipped, totalBytes); err != nil {
				return err
			}
			continue
		}
		*totalBytes += fi.Size()
		if *totalBytes > opt.MaxBytes {
			return copyTooBigError(*totalBytes, opt.MaxBytes)
		}
		*entries = append(*entries, localEntry{rootRel: childRoot, remoteRel: childRemote, isDir: false, size: fi.Size()})
	}
	return nil
}

func checkRemoteTargetsMissing(sc *sftp.Client, dstBase string, entries []localEntry) error {
	for _, e := range entries {
		if e.isDir {
			continue
		}
		target := remoteTargetPath(dstBase, e.remoteRel)
		if _, err := sc.Lstat(target); err == nil {
			return &UserError{
				Msg:  CleanPath(target) + " already exists in the session",
				Hint: "Pass -f to replace existing files.",
				Code: "cp_exists",
			}
		}
	}
	return nil
}

func uploadLocalEntries(sc *sftp.Client, root *os.Root, dstBase string, isDir bool, entries []localEntry, opt CopyOptions) (int, int64, error) {
	if isDir {
		if err := sc.MkdirAll(dstBase); err != nil {
			return 0, 0, fmt.Errorf("create %s in session: %w", dstBase, err)
		}
	}
	var copiedBytes int64
	copiedFiles := 0
	for _, e := range entries {
		target := remoteTargetPath(dstBase, e.remoteRel)
		if e.isDir {
			if err := sc.MkdirAll(target); err != nil {
				return copiedFiles, copiedBytes, fmt.Errorf("create %s in session: %w", target, err)
			}
			continue
		}
		if err := sc.MkdirAll(path.Dir(target)); err != nil {
			return copiedFiles, copiedBytes, fmt.Errorf("create parent of %s in session: %w", target, err)
		}
		in, err := root.Open(e.rootRel)
		if err != nil {
			return copiedFiles, copiedBytes, fmt.Errorf("open %s: %w", e.rootRel, err)
		}
		n, werr := writeRemoteFile(sc, target, in, opt.MaxBytes-copiedBytes, opt.Force)
		_ = in.Close()
		if werr != nil {
			return copiedFiles, copiedBytes, werr
		}
		copiedBytes += n
		copiedFiles++
	}
	return copiedFiles, copiedBytes, nil
}

func remoteTargetPath(dstBase, rel string) string {
	if rel == "" {
		return dstBase
	}
	return path.Join(dstBase, rel)
}

func writeRemoteFile(sc *sftp.Client, target string, src io.Reader, limit int64, force bool) (int64, error) {
	if !force {
		return writeRemoteFileExclusive(sc, target, src, limit)
	}
	return writeRemoteFileAtomic(sc, target, src, limit)
}

func writeRemoteFileExclusive(sc *sftp.Client, target string, src io.Reader, limit int64) (int64, error) {
	f, err := sc.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, &UserError{
				Msg:  CleanPath(target) + " already exists in the session",
				Hint: "Pass -f to replace existing files.",
				Code: "cp_exists",
			}
		}
		return 0, fmt.Errorf("create %s in session: %w", target, err)
	}
	return copyAndCloseRemote(sc, target, f, src, limit)
}

func writeRemoteFileAtomic(sc *sftp.Client, target string, src io.Reader, limit int64) (int64, error) {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := path.Join(path.Dir(target), ".cp-tmp-"+hex.EncodeToString(rnd[:]))
	f, err := sc.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, fmt.Errorf("create temp file for %s in session: %w", target, err)
	}
	n, err := copyAndCloseRemote(sc, tmp, f, src, limit)
	if err != nil {
		return n, err
	}
	if err := sc.PosixRename(tmp, target); err != nil {
		_ = sc.Remove(tmp)
		return n, fmt.Errorf("replace %s in session: %w", target, err)
	}
	return n, nil
}

func copyAndCloseRemote(sc *sftp.Client, pathToRemove string, dst *sftp.File, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	cerr := dst.Close()
	if n > limit {
		_ = sc.Remove(pathToRemove)
		return n, copyTooBigError(n, limit)
	}
	if err != nil {
		_ = sc.Remove(pathToRemove)
		return n, fmt.Errorf("write %s in session: %w", pathToRemove, err)
	}
	if cerr != nil {
		_ = sc.Remove(pathToRemove)
		return n, fmt.Errorf("close %s in session: %w", pathToRemove, cerr)
	}
	return n, nil
}
