package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
)

type seedIndexEntry struct {
	mode string
	oid  string
}

func seedIndex(ctx context.Context, git *pullGit) (map[string]seedIndexEntry, error) {
	data, err := git.run(ctx, nil, pullMetadataMax, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	entries := map[string]seedIndexEntry{}
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, name, found := strings.Cut(string(record), "\t")
		fields := strings.Split(header, " ")
		if !found || len(fields) != 3 || fields[2] != "0" || !api.ValidGitOID(fields[1]) || !files.Relative(name) {
			return nil, &UserError{Code: "seed_index", Msg: "the original index contains an unresolved or unsafe entry", Hint: "Resolve index conflicts before using --seed-dirty.", Kind: KindRefused}
		}
		switch fields[0] {
		case "100644", "100755", "120000", "160000":
		default:
			return nil, copyInputError("seed_index", "the original index has an unsupported object mode")
		}
		entries[name] = seedIndexEntry{mode: fields[0], oid: fields[1]}
	}
	return entries, nil
}

// dirtySeedCommit constructs one extra tree through an entirely private index.
// The only source-repository writes are new unreachable Git objects. Neither
// index, refs, worktree files nor the selected branch are changed.
func dirtySeedCommit(ctx context.Context, git *pullGit, scratch, parent string, identity api.GitIdentity) (string, []string, error) {
	if !identity.Known || identity.Name == "" || identity.Email == "" {
		return "", nil, &UserError{Code: "seed_identity", Msg: "--seed-dirty needs a laptop Git name and email", Hint: "Set user.name and user.email in your laptop Git configuration.", Kind: KindUsage}
	}
	index, err := seedIndex(ctx, git)
	if err != nil {
		return "", nil, err
	}
	listed, err := git.run(ctx, nil, pullMetadataMax, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return "", nil, err
	}
	root, err := os.OpenRoot(git.root)
	if err != nil {
		return "", nil, err
	}
	defer root.Close()
	private := *git
	private.role = localGitDirty
	private.env, err = git.metadata.environment(ctx, localGitDirty, filepath.Join(scratch, "index"), identity)
	if err != nil {
		return "", nil, err
	}
	if _, err = private.run(ctx, nil, 4096, "read-tree", "--empty"); err != nil {
		return "", nil, err
	}
	seen := map[string]bool{}
	var records bytes.Buffer
	var untracked []string
	folders := privateFolderInfos()
	for _, item := range bytes.Split(listed, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		name := string(item)
		if !files.Relative(name) {
			return "", nil, copyInputError("copy_sensitive", "a dirty seed has an unsafe source path")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		st, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue // a missing tracked file is deliberately not read
		}
		if err != nil {
			return "", nil, err
		}
		if err = dirtySeedSensitive(root, name, folders); err != nil {
			return "", nil, err
		}
		entry, tracked := index[name]
		mode, oid := "100644", ""
		switch {
		case tracked && entry.mode == "160000" && st.IsDir():
			// A gitlink retains the original index object, without entering or
			// running Git inside a submodule's separate working tree.
			mode, oid = entry.mode, entry.oid
		case st.Mode()&os.ModeSymlink != 0:
			link, err := root.Readlink(name)
			if err != nil {
				return "", nil, err
			}
			after, err := root.Lstat(name)
			if err != nil || !os.SameFile(st, after) {
				return "", nil, copyInputError("seed_changed", "a dirty seed link changed while reading")
			}
			oid, err = seedHashObject(ctx, &private, strings.NewReader(link))
			if err != nil {
				return "", nil, err
			}
			mode = "120000"
		case st.Mode().IsRegular():
			f, err := files.OpenContainedRegular(root, name)
			if err != nil {
				return "", nil, err
			}
			opened, statErr := f.Stat()
			if statErr != nil || !os.SameFile(st, opened) {
				f.Close()
				return "", nil, copyInputError("seed_changed", "a dirty seed file changed before opening")
			}
			if err = checkCopyPath(root, name, filepath.Join(git.root, filepath.FromSlash(name)), f, folders); err == nil {
				oid, err = seedHashObject(ctx, &private, &inputContextReader{ctx: ctx, reader: io.LimitReader(f, st.Size()+1)})
			}
			after, statErr := f.Stat()
			f.Close()
			if err != nil {
				return "", nil, err
			}
			if statErr != nil || after.Size() != st.Size() || !after.ModTime().Equal(st.ModTime()) {
				return "", nil, copyInputError("seed_changed", "a dirty seed file changed during hashing")
			}
			if st.Mode().Perm()&0111 != 0 {
				mode = "100755"
			}
		default:
			return "", nil, copyInputError("seed_special", "a dirty seed contains a special file")
		}
		fmt.Fprintf(&records, "%s %s\t%s%c", mode, oid, name, byte(0))
		if !tracked {
			untracked = append(untracked, name)
		}
	}
	if _, err = private.run(ctx, records.Bytes(), 4096, "update-index", "--add", "-z", "--index-info"); err != nil {
		return "", nil, err
	}
	tree, err := private.run(ctx, nil, 4096, "write-tree")
	if err != nil {
		return "", nil, fmt.Errorf("write private seed tree: %w", err)
	}
	if !api.ValidGitOID(gitLine(tree)) {
		return "", nil, copyInputError("seed_tree", "Git returned an invalid private tree identity")
	}
	commit, err := private.run(ctx, []byte("Capture laptop working files\n"), 4096, "commit-tree", "--no-gpg-sign", gitLine(tree), "-p", parent)
	if err != nil {
		return "", nil, fmt.Errorf("write private seed commit: %w", err)
	}
	if !api.ValidGitOID(gitLine(commit)) {
		return "", nil, copyInputError("seed_commit", "Git returned an invalid private commit identity")
	}
	slices.Sort(untracked)
	return gitLine(commit), untracked, nil
}

func seedHashObject(ctx context.Context, git *pullGit, input io.Reader) (string, error) {
	output := &cappedGitOutput{limit: 4096}
	if err := git.runIO(ctx, input, output, "hash-object", "--no-filters", "-w", "--stdin"); err != nil {
		return "", err
	}
	oid := gitLine(output.Bytes())
	if !api.ValidGitOID(oid) {
		return "", copyInputError("seed_object", "Git returned an invalid dirty seed object identity")
	}
	return oid, nil
}

func dirtySeedSensitive(root *os.Root, name string, folders []privateFolder) error {
	for _, component := range strings.Split(name, "/") {
		if privateName(component) != "" {
			return copyInputError("copy_sensitive", "a dirty seed path has a sensitive name")
		}
	}
	typed := filepath.Join(root.Name(), filepath.FromSlash(name))
	if inPrivateFolder(typed, folders) != "" {
		return copyInputError("copy_sensitive", "a dirty seed path lies in a private folder")
	}
	resolved, err := filepath.EvalSymlinks(typed)
	if os.IsNotExist(err) {
		return nil // a broken symlink contributes only its literal target
	}
	if err != nil {
		return err
	}
	if privateName(filepath.Base(resolved)) != "" || inPrivateFolder(resolved, folders) != "" {
		return copyInputError("copy_sensitive", "a dirty seed path aliases a sensitive source")
	}
	return nil
}
