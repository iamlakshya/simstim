package client

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/review"
)

// CopyOptions controls CopyFromSFTP and CopyToSFTP.
type CopyOptions struct {
	Force        bool
	AllowFlagged bool
	MaxBytes     int64
	MaxEntries   int
	// ConfirmFlagged, when non-nil and AllowFlagged is false, asks the user
	// whether to continue when flagged files are found. Returning false cancels.
	ConfirmFlagged func(res review.Result) (bool, error)
}

func (o CopyOptions) withDefaults() CopyOptions {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultCPMaxBytes
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = DefaultCPMaxEntries
	}
	return o
}

// CopyResult reports what a copy operation did.
type CopyResult struct {
	Files   int
	Bytes   int64
	Skipped []string
}

type remoteEntry struct {
	remotePath string
	relPath    string // relative to copy root ("" for a single file)
	isDir      bool
	size       int64
}

type stagedFile struct {
	tmpRel  string
	destRel string
}

// CopyFromSFTP copies remotePath (a file or directory) from sc to localDst on
// the laptop safely using os.Root, enforcing caps, refusing symlinks and .git
// paths, and reviewing flagged files (spec 21.1).
func CopyFromSFTP(sc *sftp.Client, remotePath, localDst string, opt CopyOptions) (CopyResult, error) {
	opt = opt.withDefaults()
	srcPath, st, entries, skipped, err := listRemoteSource(sc, remotePath, opt)
	if err != nil {
		return CopyResult{}, err
	}

	childName := path.Base(srcPath)
	if childName == "" || childName == "/" || childName == "." {
		childName = "copy"
	}
	root, baseRel, err := openRootForTarget(localDst, childName)
	if err != nil {
		return CopyResult{}, err
	}
	defer root.Close()
	if err := validateRelEntry(baseRel); err != nil {
		return CopyResult{}, err
	}

	destRels, pathFindings, err := planRemoteCopy(root, baseRel, entries, opt)
	if err != nil {
		return CopyResult{}, err
	}
	// Fast-fail before downloading any bytes when non-interactive and a path is flagged.
	if pathFindings.Flagged() && !opt.AllowFlagged && opt.ConfirmFlagged == nil {
		return CopyResult{}, handleFlagged(pathFindings, opt)
	}

	staged, createdDirs, copiedBytes, contentFindings, err := stageRemoteCopy(sc, root, baseRel, st.IsDir(), entries, destRels, opt)
	if err != nil {
		return CopyResult{}, err
	}
	cleanup := func() { cleanupStaged(root, staged, createdDirs) }

	allFindings := review.Result{
		Findings: append(append([]review.Finding(nil), pathFindings.Findings...), contentFindings.Findings...),
	}
	if allFindings.Flagged() && !opt.AllowFlagged {
		if err := handleFlagged(allFindings, opt); err != nil {
			cleanup()
			return CopyResult{}, err
		}
	}
	for _, s := range staged {
		if err := commitTempInRoot(root, s.tmpRel, s.destRel, opt.Force); err != nil {
			cleanup()
			return CopyResult{}, err
		}
	}
	return CopyResult{Files: len(staged), Bytes: copiedBytes, Skipped: skipped}, nil
}

func listRemoteSource(sc *sftp.Client, remotePath string, opt CopyOptions) (string, fs.FileInfo, []remoteEntry, []string, error) {
	srcPath, err := resolveRemotePath(sc, remotePath)
	if err != nil {
		return "", nil, nil, nil, err
	}
	st, err := sc.Lstat(srcPath)
	if err != nil {
		return "", nil, nil, nil, &UserError{
			Msg:  fmt.Sprintf("cannot find %s in the session: %v", CleanPath(remotePath), err),
			Hint: "Check the path in the session and try again.",
			Code: "cp_not_found",
			Kind: KindNotFound,
		}
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return "", nil, nil, nil, &UserError{
			Msg:  CleanPath(remotePath) + " is a symbolic link; cp copies regular files and folders only",
			Hint: "Copy the target file directly.",
			Code: "cp_path",
			Kind: KindRefused,
		}
	}
	if !st.IsDir() && !st.Mode().IsRegular() {
		return "", nil, nil, nil, &UserError{
			Msg:  CleanPath(remotePath) + " is not a regular file or folder",
			Hint: "Copy a regular file or folder.",
			Code: "cp_path",
			Kind: KindRefused,
		}
	}
	var entries []remoteEntry
	var skipped []string
	var totalBytes int64
	if st.IsDir() {
		if err := walkSFTP(sc, srcPath, "", opt, &entries, &skipped, &totalBytes); err != nil {
			return "", nil, nil, nil, err
		}
		return srcPath, st, entries, skipped, nil
	}
	if st.Size() > opt.MaxBytes {
		return "", nil, nil, nil, copyTooBigError(st.Size(), opt.MaxBytes)
	}
	entries = []remoteEntry{{remotePath: srcPath, relPath: "", isDir: false, size: st.Size()}}
	return srcPath, st, entries, skipped, nil
}

func planRemoteCopy(root *os.Root, baseRel string, entries []remoteEntry, opt CopyOptions) ([]string, review.Result, error) {
	destRels := make([]string, 0, len(entries))
	var pathFindings review.Result
	for _, e := range entries {
		if err := validateRelEntry(e.relPath); err != nil {
			return nil, review.Result{}, err
		}
		dest := baseRel
		if e.relPath != "" {
			dest = baseRel + "/" + e.relPath
		}
		if err := validateRelEntry(dest); err != nil {
			return nil, review.Result{}, err
		}
		destRels = append(destRels, dest)
		if !e.isDir {
			if cat, ok := review.CheckPath(dest); ok {
				pathFindings.Findings = append(pathFindings.Findings, review.Finding{Path: dest, Category: cat})
			}
			if e.size > review.MaxScanSize {
				pathFindings.Findings = append(pathFindings.Findings, review.Finding{Path: dest, Category: "large", Detail: formatCopySize(e.size)})
			}
		}
	}
	if err := checkCaseCollisions(destRels); err != nil {
		return nil, review.Result{}, err
	}
	for i, e := range entries {
		dest := destRels[i]
		if err := ensureNoSymlinkAncestors(root, dest); err != nil {
			return nil, review.Result{}, err
		}
		if !e.isDir && !opt.Force {
			if _, err := root.Lstat(dest); err == nil {
				return nil, review.Result{}, &UserError{
					Msg:  CleanPath(dest) + " already exists on your laptop",
					Hint: "Pass -f to replace existing files.",
					Code: "cp_exists",
				}
			}
		}
	}
	return destRels, pathFindings, nil
}

func stageRemoteCopy(sc *sftp.Client, root *os.Root, baseRel string, isDir bool, entries []remoteEntry, destRels []string, opt CopyOptions) (staged []stagedFile, createdDirs []string, copiedBytes int64, contentFindings review.Result, err error) {
	cleanup := func() { cleanupStaged(root, staged, createdDirs) }
	if isDir {
		if err := mkdirAllNoSymlink(root, baseRel, &createdDirs); err != nil {
			cleanup()
			return nil, nil, 0, review.Result{}, err
		}
	}
	for i, e := range entries {
		dest := destRels[i]
		if e.isDir {
			if err := mkdirAllNoSymlink(root, dest, &createdDirs); err != nil {
				cleanup()
				return nil, nil, 0, review.Result{}, err
			}
			continue
		}
		tmp, werr := stageOneRemoteFile(sc, root, e, dest, opt.MaxBytes-copiedBytes, &createdDirs)
		if werr != nil {
			cleanup()
			if errors.Is(werr, errCopyTooBig) {
				return nil, nil, 0, review.Result{}, copyTooBigError(copiedBytes+tmp.written, opt.MaxBytes)
			}
			return nil, nil, 0, review.Result{}, werr
		}
		copiedBytes += tmp.written
		staged = append(staged, stagedFile{tmpRel: tmp.tmpRel, destRel: dest})

		if strings.EqualFold(path.Base(dest), ".gitmodules") && tmp.hasCR {
			cleanup()
			return nil, nil, 0, review.Result{}, &UserError{
				Msg:  fmt.Sprintf("refused %s: .gitmodules contains a carriage return (CVE-2025-48384)", CleanPath(dest)),
				Hint: "Fix line endings in .gitmodules inside the session before copying.",
				Code: "cp_flagged",
				Kind: KindRefused,
			}
		}
		if review.CheckHiddenText(tmp.preview) {
			contentFindings.Findings = append(contentFindings.Findings, review.Finding{
				Path:     dest,
				Category: "hidden text",
				Detail:   "hidden Unicode characters",
			})
		}
	}
	return staged, createdDirs, copiedBytes, contentFindings, nil
}

func stageOneRemoteFile(sc *sftp.Client, root *os.Root, e remoteEntry, dest string, remaining int64, createdDirs *[]string) (stagedTemp, error) {
	rf, err := sc.Open(e.remotePath)
	if err != nil {
		return stagedTemp{}, fmt.Errorf("read %s from session: %w", e.remotePath, err)
	}
	defer rf.Close()
	return writeTempInRoot(root, dest, rf, e.size, remaining, review.MaxScanSize, createdDirs)
}

func cleanupStaged(root *os.Root, staged []stagedFile, createdDirs []string) {
	for _, s := range staged {
		_ = root.Remove(s.tmpRel)
	}
	for i := len(createdDirs) - 1; i >= 0; i-- {
		_ = root.Remove(createdDirs[i])
	}
}

func walkSFTP(sc *sftp.Client, curRemote, curRel string, opt CopyOptions, out *[]remoteEntry, skipped *[]string, total *int64) error {
	list, err := sc.ReadDir(curRemote)
	if err != nil {
		return fmt.Errorf("list %s in session: %w", curRemote, err)
	}
	for _, fi := range list {
		name := fi.Name()
		childRemote := path.Join(curRemote, name)
		childRel := name
		if curRel != "" {
			childRel = curRel + "/" + name
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			*skipped = append(*skipped, childRel+" (symbolic link)")
			continue
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			*skipped = append(*skipped, childRel+" (special file)")
			continue
		}
		if len(*out)+1 > opt.MaxEntries {
			return &UserError{
				Msg:  fmt.Sprintf("the copy holds more than %d entries", opt.MaxEntries),
				Hint: "Copy a smaller folder, or use " + brand.Name + " pull for a git repository.",
				Code: "cp_too_big",
				Kind: KindRefused,
			}
		}
		if fi.IsDir() {
			*out = append(*out, remoteEntry{remotePath: childRemote, relPath: childRel, isDir: true})
			if err := walkSFTP(sc, childRemote, childRel, opt, out, skipped, total); err != nil {
				return err
			}
			continue
		}
		*total += fi.Size()
		if *total > opt.MaxBytes {
			return copyTooBigError(*total, opt.MaxBytes)
		}
		*out = append(*out, remoteEntry{remotePath: childRemote, relPath: childRel, isDir: false, size: fi.Size()})
	}
	return nil
}

func resolveRemotePath(sc *sftp.Client, p string) (string, error) {
	if p == "" || p == "." {
		wd, err := sc.Getwd()
		if err != nil {
			return "", fmt.Errorf("read session workspace path: %w", err)
		}
		return wd, nil
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	wd, err := sc.Getwd()
	if err != nil {
		return "", fmt.Errorf("read session workspace path: %w", err)
	}
	return path.Join(wd, p), nil
}

func handleFlagged(res review.Result, opt CopyOptions) error {
	if opt.ConfirmFlagged != nil {
		ok, err := opt.ConfirmFlagged(res)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return &UserError{
		Msg:  "the copy holds flagged files: " + CleanLine(res.Summary()),
		Hint: "Read them in the session, then run cp again with --allow-flagged.",
		Code: "cp_flagged",
		Kind: KindRefused,
	}
}

func copyTooBigError(size, maxBytes int64) error {
	return &UserError{
		Msg:  fmt.Sprintf("the copy holds %s, and cp copies at most %s", formatCopySize(size), formatCopySize(maxBytes)),
		Hint: brand.Name + " pull <host>/<session> for work in git, or copy fewer files.",
		Code: "cp_too_big",
		Kind: KindRefused,
	}
}

func formatCopySize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
