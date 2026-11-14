package client

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// localGitRecipe recognises the closed operations of the existing executor.
// It is a launch gate, not a second argument or configuration policy. The one
// gitpolicy.Args definition is applied in the guardian after this check.
func localGitRecipe(args []string) bool {
	if slices.Equal(args, []string{"version"}) ||
		slices.Equal(args, []string{"ls-files", "--stage", "-z"}) ||
		slices.Equal(args, []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}) ||
		slices.Equal(args, []string{"read-tree", "--empty"}) ||
		slices.Equal(args, []string{"update-index", "--add", "-z", "--index-info"}) ||
		slices.Equal(args, []string{"write-tree"}) ||
		slices.Equal(args, []string{"hash-object", "--no-filters", "-w", "--stdin"}) ||
		slices.Equal(args, []string{"hash-object", "-w", "-t", "tree", "--stdin"}) ||
		slices.Equal(args, []string{"worktree", "list", "--porcelain", "-z"}) ||
		slices.Equal(args, []string{"symbolic-ref", "--quiet", "--short", "HEAD"}) ||
		slices.Equal(args, []string{"config", "--get", "remote.origin.url"}) ||
		slices.Equal(args, []string{"config", "--includes", "--null", "--get-regexp", `^user\.(name|email)$`}) {
		return true
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "ls-tree":
		return len(args) == 5 && args[1] == "-z" && api.ValidGitOID(args[2]) && args[3] == "--" && (args[4] == "."+brand.Name || args[4] == "."+brand.Name+"/sandbox.toml")
	case "rev-parse":
		return localGitRevisionRecipe(args[1:])
	case "cat-file":
		return len(args) == 3 && slices.Contains([]string{"-e", "-t", "-s", "blob"}, args[1]) && localGitObject(args[2])
	case "check-ref-format":
		return len(args) == 2 && localGitRef(args[1])
	case "merge-base":
		return len(args) == 4 && args[1] == "--is-ancestor" && localGitObject(args[2]) && localGitObject(args[3])
	case "rev-list":
		return len(args) == 4 && args[1] == "--count" && localGitRef(args[2]) && args[3] == "^HEAD"
	case "bundle":
		return len(args) == 3 && slices.Contains([]string{"verify", "list-heads"}, args[1]) && filepath.IsAbs(args[2]) ||
			len(args) == 4 && args[1] == "create" && args[2] == "-" && localGitRef(args[3])
	case "init":
		return len(args) == 5 && args[1] == "--bare" && args[2] == "--template=" &&
			slices.Contains([]string{"--object-format=sha1", "--object-format=sha256"}, args[3]) && filepath.IsAbs(args[4])
	case "commit-tree":
		return len(args) == 5 && args[1] == "--no-gpg-sign" && api.ValidGitOID(args[2]) && args[3] == "-p" && api.ValidGitOID(args[4])
	case "update-ref":
		return len(args) == 3 && localGitRef(args[1]) && api.ValidGitOID(args[2]) ||
			len(args) == 7 && slices.Equal(args[1:4], []string{"--no-deref", "-m", "pull reviewed session work"}) &&
				localGitRef(args[4]) && api.ValidGitOID(args[5]) && api.ValidGitOID(args[6])
	case "config":
		return len(args) == 5 && args[1] == "--no-includes" && args[2] == "--null" &&
			strings.HasPrefix(args[3], "--blob=") && api.ValidGitOID(strings.TrimPrefix(args[3], "--blob=")) && args[4] == "--list"
	case "diff-tree":
		return len(args) == 9 && slices.Equal(args[1:7], []string{"-r", "--no-renames", "--no-commit-id", "--no-abbrev", "--raw", "-z"}) &&
			localGitObject(args[7]) && localGitObject(args[8])
	case "diff":
		return len(args) == 10 && slices.Equal(args[1:6], []string{"--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", "--no-color"}) &&
			localGitObject(args[6]) && localGitObject(args[7]) && args[8] == "--" && args[9] != ""
	case "-c":
		return len(args) == 12 && slices.Equal(args[:10], []string{"-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "fetch",
			"--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--atomic", "--"}) &&
			filepath.IsAbs(args[10]) && strings.HasPrefix(args[11], "refs/heads/") && strings.Contains(args[11], "*:refs/")
	}
	return false
}

func localGitRevisionRecipe(args []string) bool {
	if slices.Equal(args, []string{"--show-toplevel"}) || slices.Equal(args, []string{"--absolute-git-dir"}) ||
		slices.Equal(args, []string{"--path-format=absolute", "--git-path", "objects"}) {
		return true
	}
	return len(args) == 3 && slices.Equal(args[:2], []string{"--verify", "--end-of-options"}) && args[2] != "" ||
		len(args) == 4 && slices.Equal(args[:3], []string{"--symbolic-full-name", "--verify", "--end-of-options"}) && args[3] != ""
}

func localGitObject(value string) bool {
	return api.ValidGitOID(value) || api.ValidGitOID(strings.TrimSuffix(value, "^{commit}")) || localGitRef(value)
}

func localGitRef(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "\x00\r\n")
}
