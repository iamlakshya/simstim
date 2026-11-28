package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// buildDirtySeedBundle implements spec 17.4's clean-filter-free, ref-free
// dirty snapshot bundling using a temporary GIT_INDEX_FILE, git hash-object
// --no-filters -w, git update-index -z --index-info, write-tree, commit-tree
// and a temporary bare repo referencing the laptop's object store via alternates.
func buildDirtySeedBundle(ctx context.Context, top, branch, userName, userEmail, tmpDir string, idx int, bundlePath string, out io.Writer) ([]string, error) {
	untracked, err := checkAndPrintUntracked(ctx, top, out)
	if err != nil {
		return nil, err
	}
	indexInfo, err := collectDirtyIndexInfo(ctx, top)
	if err != nil {
		return nil, err
	}
	tip, err := commitDirtyTree(ctx, top, branch, userName, userEmail, tmpDir, idx, indexInfo)
	if err != nil {
		return nil, err
	}
	if err := bundleFromTempBare(ctx, top, branch, tip, tmpDir, idx, bundlePath); err != nil {
		return nil, err
	}
	return untracked, nil
}

func checkAndPrintUntracked(ctx context.Context, top string, out io.Writer) ([]string, error) {
	untrackedRaw, err := seedGitBytes(ctx, top, nil, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list untracked files in %s: %w", top, err)
	}
	untracked := splitNULPaths(untrackedRaw)
	sort.Strings(untracked)
	for _, rel := range untracked {
		full := filepath.Join(top, filepath.FromSlash(rel))
		if why := CheckSensitiveLocalPath(full); why != "" {
			return nil, &UserError{
				Msg:  fmt.Sprintf("refused to seed %s: %s", CleanPath(rel), why),
				Hint: "Add private files to .gitignore or remove them before using --seed-dirty.",
				Code: "copy_sensitive",
				Kind: KindRefused,
			}
		}
	}
	if len(untracked) > 0 {
		fmt.Fprintf(out, "Sending %d untracked file(s) from %s:\n", len(untracked), filepath.Base(top))
		for _, rel := range untracked {
			fmt.Fprintf(out, "  + %s\n", CleanLine(rel))
		}
	}
	return untracked, nil
}

func collectDirtyIndexInfo(ctx context.Context, top string) ([]byte, error) {
	gitlinks, err := readGitlinkEntries(ctx, top)
	if err != nil {
		return nil, err
	}
	allRaw, err := seedGitBytes(ctx, top, nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list files in %s: %w", top, err)
	}
	allPaths := dedupStrings(splitNULPaths(allRaw))

	var indexInfo bytes.Buffer
	for _, rel := range allPaths {
		if oid, ok := gitlinks[rel]; ok {
			fmt.Fprintf(&indexInfo, "160000 %s\t%s\x00", oid, rel)
			continue
		}
		line, ok, err := dirtyIndexLine(ctx, top, rel)
		if err != nil {
			return nil, err
		}
		if ok {
			indexInfo.WriteString(line)
		}
	}
	return indexInfo.Bytes(), nil
}

func dirtyIndexLine(ctx context.Context, top, rel string) (string, bool, error) {
	full := filepath.Join(top, filepath.FromSlash(rel))
	st, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			// A tracked file missing from the working tree stays out (spec 17.4).
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat %s: %w", rel, err)
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return "", false, fmt.Errorf("readlink %s: %w", rel, err)
		}
		oid, err := hashObjectStdin(ctx, top, []byte(target))
		if err != nil {
			return "", false, fmt.Errorf("hash symlink %s: %w", rel, err)
		}
		return fmt.Sprintf("120000 %s\t%s\x00", oid, rel), true, nil
	case st.Mode().IsRegular():
		oid, err := seedGitOut(ctx, top, nil, "hash-object", "--no-filters", "-w", "--", full)
		if err != nil {
			return "", false, fmt.Errorf("hash %s: %w", rel, err)
		}
		mode := "100644"
		if st.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
		return fmt.Sprintf("%s %s\t%s\x00", mode, strings.TrimSpace(oid), rel), true, nil
	default:
		return "", false, nil
	}
}

func commitDirtyTree(ctx context.Context, top, branch, userName, userEmail, tmpDir string, idx int, indexInfo []byte) (string, error) {
	tmpIndex := filepath.Join(tmpDir, fmt.Sprintf("index-%d", idx))
	idxEnv := []string{"GIT_INDEX_FILE=" + tmpIndex}
	if err := seedGitStdin(ctx, top, idxEnv, indexInfo, "update-index", "-z", "--index-info"); err != nil {
		return "", fmt.Errorf("populate temporary index: %w", err)
	}
	tree, err := seedGitOut(ctx, top, idxEnv, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write dirty tree: %w", err)
	}
	tree = strings.TrimSpace(tree)
	parent, err := seedGitOut(ctx, top, nil, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	parent = strings.TrimSpace(parent)
	headTree, _ := seedGitOut(ctx, top, nil, "rev-parse", "refs/heads/"+branch+"^{tree}")
	if tree == strings.TrimSpace(headTree) {
		return parent, nil
	}
	authorName := userName
	if authorName == "" {
		authorName = "sandbox"
	}
	authorEmail := userEmail
	if authorEmail == "" {
		authorEmail = "sandbox@localhost"
	}
	commitEnv := []string{
		"GIT_AUTHOR_NAME=" + authorName,
		"GIT_AUTHOR_EMAIL=" + authorEmail,
		"GIT_COMMITTER_NAME=" + authorName,
		"GIT_COMMITTER_EMAIL=" + authorEmail,
	}
	commit, err := seedGitOut(ctx, top, commitEnv, "commit-tree", tree, "-p", parent, "-m", "WIP: uncommitted changes from laptop")
	if err != nil {
		return "", fmt.Errorf("commit dirty tree: %w", err)
	}
	return strings.TrimSpace(commit), nil
}

func bundleFromTempBare(ctx context.Context, top, branch, tip, tmpDir string, idx int, bundlePath string) error {
	objDir, err := seedGitOut(ctx, top, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return fmt.Errorf("locate git objects folder: %w", err)
	}
	bareDir := filepath.Join(tmpDir, fmt.Sprintf("bare-%d.git", idx))
	if _, err := seedGitOut(ctx, tmpDir, nil, "init", "--bare", "--quiet", bareDir); err != nil {
		return fmt.Errorf("init temporary bare repo: %w", err)
	}
	altPath := filepath.Join(bareDir, "objects", "info", "alternates")
	if err := os.WriteFile(altPath, []byte(strings.TrimSpace(objDir)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write alternates: %w", err)
	}
	if _, err := seedGitOut(ctx, bareDir, nil, "update-ref", "refs/heads/"+branch, tip); err != nil {
		return fmt.Errorf("update temporary bare ref: %w", err)
	}
	if _, err := seedGitOut(ctx, bareDir, nil, "bundle", "create", bundlePath, "refs/heads/"+branch); err != nil {
		return fmt.Errorf("create dirty seed bundle: %w", err)
	}
	return nil
}

func readGitlinkEntries(ctx context.Context, top string) (map[string]string, error) {
	raw, err := seedGitBytes(ctx, top, nil, "ls-files", "-z", "--stage")
	if err != nil {
		return nil, fmt.Errorf("read git index: %w", err)
	}
	out := map[string]string{}
	for _, rec := range bytes.Split(raw, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, rel, ok := strings.Cut(string(rec), "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) >= 2 && fields[0] == "160000" {
			out[rel] = fields[1]
		}
	}
	return out, nil
}

func splitNULPaths(raw []byte) []string {
	var out []string
	for _, b := range bytes.Split(raw, []byte{0}) {
		if len(b) > 0 {
			out = append(out, string(b))
		}
	}
	return out
}

func dedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func hashObjectStdin(ctx context.Context, dir string, data []byte) (string, error) {
	cmd := seedGitCmd(ctx, dir, nil, "hash-object", "--no-filters", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(data)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
