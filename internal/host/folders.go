package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
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

// checkFolder checks a new session's folder, and reports whether it is missing
// and may be made. New makes it only after its other checks pass, so a start
// that fails leaves no folder behind. A missing folder without
// NewArgs.MakeFolder is a question for the user (api.CodeNoFolder).
func checkFolder(cwd string, a NewArgs) (missing bool, err error) {
	st, err := os.Stat(cwd)
	switch {
	case err == nil && st.IsDir():
		return false, nil
	case err == nil:
		return false, UserError("bad_folder", cwd+" on this host is a file, not a folder", "Pick a folder.")
	case !errors.Is(err, fs.ErrNotExist):
		return false, UserError("bad_folder", fmt.Sprintf("cannot open the folder %s on this host: %v", cwd, err), "Check its permissions, or pick another folder.")
	case a.Worktree:
		return false, UserError("bad_folder", fmt.Sprintf("the folder %s does not exist on this host, and a new worktree needs a git repository", cwd), "Pick the folder of a repository, or start without a worktree.")
	case !a.MakeFolder:
		data, _ := json.Marshal(api.FolderQuestion{Folder: cwd})
		return false, &proto.Error{Code: api.CodeNoFolder,
			Message: fmt.Sprintf("the folder %s does not exist on this host", cwd),
			Hint:    "Pass --mkdir to make it, or pick an existing folder. The launcher asks.",
			Data:    data}
	}
	return true, nil
}
