package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// FoldersResult is what the new-session dialog completes folders from.
type FoldersResult = api.FoldersResult

// Folders completes a folder path on this host, and suggests the folders of
// recent sessions and the git repositories under the home folder. Paths under
// the home folder are written with ~.
func Folders(prefix string, limit int) FoldersResult {
	if limit <= 0 || limit > 50 {
		limit = 12
	}
	home, _ := os.UserHomeDir()
	tilde := func(p string) string {
		if p == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(p, home+"/"); ok {
			return "~/" + rest
		}
		return p
	}
	res := FoldersResult{Matches: []string{}, Recent: []string{}, Repos: []string{}}
	if prefix != "" {
		res.Matches = folderMatches(res.Matches, prefix, limit, tilde)
	}
	res.Recent = recentFolders(res.Recent, tilde)
	res.Repos = gitRepos(res.Repos, home, limit, tilde)
	sort.Strings(res.Repos)
	return res
}

// folderMatches adds to out the folders that complete prefix, up to limit.
func folderMatches(out []string, prefix string, limit int, tilde func(string) string) []string {
	full := expandHome(prefix)
	dir, base := full, ""
	if !strings.HasSuffix(prefix, "/") {
		dir, base = filepath.Dir(full), filepath.Base(full)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, tilde(p)+"/")
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

// recentFolders adds to out the folders of recent sessions, newest first, up
// to 8. A worktree counts as its repository.
func recentFolders(out []string, tilde func(string) string) []string {
	list, err := List()
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for _, s := range list {
		d := s.Cwd
		if m, err := LoadManifest(s.ID); err == nil && m.Git != nil {
			d = m.Git.RepoRoot
		}
		if t := tilde(d); !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// gitRepos adds to out the git repositories under home, three levels deep,
// up to limit, within a small time budget.
func gitRepos(out []string, home string, limit int, tilde func(string) string) []string {
	deadline := time.Now().Add(300 * time.Millisecond)
	skip := map[string]bool{"node_modules": true, "vendor": true, "target": true, "dist": true, "build": true}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 3 || len(out) >= limit || time.Now().After(deadline) {
			return
		}
		if dir == paths.DataDir() || dir == paths.StateDir() {
			return
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil && dir != home {
			out = append(out, tilde(dir))
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() || strings.HasPrefix(n, ".") || skip[n] {
				continue
			}
			walk(filepath.Join(dir, n), depth+1)
		}
	}
	walk(home, 0)
	return out
}

// checkFolder checks a new session's folder, cwd, with ~ expanded. It returns
// the folders that New must make for it, the deepest first. The list is empty
// when cwd exists. New can make only a full path. A missing ~bob/x or src/app
// stays an error, so New never makes a folder named ~bob, or one under the
// folder that the helper runs in. A new worktree needs an existing repository.
func checkFolder(cwd string, worktree bool) ([]string, error) {
	var missing []string
	for p := cwd; p != filepath.Dir(p); p = filepath.Dir(p) {
		st, err := os.Stat(p)
		if err == nil && st.IsDir() {
			break
		}
		switch {
		case err == nil:
			return nil, UserError("bad_folder", p+" on this host is a file, not a folder", "Pick another folder.")
		case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
			return nil, UserError("bad_folder", fmt.Sprintf("cannot open the folder %s on this host: %v", p, err), "Check its permissions, or pick another folder.")
		}
		missing = append(missing, p)
	}
	switch {
	case len(missing) == 0:
		return nil, nil
	case !filepath.IsAbs(cwd):
		return nil, UserError("bad_folder", fmt.Sprintf("the folder %s does not exist on this host", cwd), "Pick an existing folder. To make a new one, give its full path, such as ~/src/app.")
	case worktree:
		return nil, UserError("bad_folder", fmt.Sprintf("the folder %s does not exist on this host, and a new worktree needs a git repository", cwd), "Pick the folder of a repository, or start without a worktree.")
	}
	return missing, nil
}

// folderQuestion asks the user whether New may make the missing folder cwd.
func folderQuestion(cwd string) error {
	return questionError(api.CodeNoFolder, fmt.Sprintf("the folder %s does not exist on this host", cwd),
		"Pass --mkdir to make it, or pick an existing folder. The launcher asks.", api.FolderQuestion{Folder: cwd})
}

// makeFolders makes a new session's folder, cwd, and the missing folders
// above it. The umask sets their mode, as it does for mkdir -p.
func makeFolders(cwd string) error {
	//nolint:gosec // G301: a folder that the user asked for gets the mode that mkdir -p gives it
	if err := os.MkdirAll(cwd, 0o777); err != nil {
		return UserError("bad_folder", fmt.Sprintf("could not make the folder %s: %v", cwd, err), "Check that you may write to the folder above it, or pick another folder.")
	}
	return nil
}

// removeFolders removes the folders that New made for a start that then
// failed, the deepest first. It removes only empty folders, so it never
// removes a file that the start, or anything else, put in them.
func removeFolders(made []string) {
	for _, d := range made {
		if os.Remove(d) != nil {
			return
		}
	}
}
