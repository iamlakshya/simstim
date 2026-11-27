package guest

import (
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

type purgeReaderGitRequestV1 struct {
	Directory string   `json:"directory"`
	Arguments []string `json:"arguments"`
}

const purgeReaderGitOperationV1 = "purge.reader-git.v1"

// No wire argv is an execution interface. The actual inspector selects these
// closed probes; the private helper checks that same grammar before exec.
func validatePurgeReaderGitV1(request purgeReaderGitRequestV1) error {
	name, ok := strings.CutPrefix(request.Directory, "/work/")
	if !ok || !api.InputRepoName(name) || path.Clean(request.Directory) != request.Directory {
		return purgeWorkErrorV1("the fixed Git inspection has no protected workspace")
	}
	a := request.Arguments
	fixed := [][]string{
		{"--version"}, {"rev-parse", "--show-toplevel"},
		{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none"},
		{"rev-parse", "--verify", "--quiet", "HEAD^{commit}"}, {"symbolic-ref", "--quiet", "HEAD"},
	}
	for _, wanted := range fixed {
		if equalPurgeReaderWordsV1(a, wanted) {
			return nil
		}
	}
	if len(a) == 3 && a[0] == "cat-file" && a[1] == "-e" && strings.HasSuffix(a[2], "^{commit}") && api.ValidGitOID(strings.TrimSuffix(a[2], "^{commit}")) {
		return nil
	}
	if len(a) == 4 && equalPurgeReaderWordsV1(a[:3], []string{"show-ref", "--verify", "--quiet"}) && purgeReaderHeadRefV1(a[3]) {
		return nil
	}
	if len(a) >= 4 && len(a) <= 1200 && equalPurgeReaderWordsV1(a[:3], []string{"rev-list", "--count", "--all"}) {
		at := 3
		if api.ValidGitOID(a[at]) {
			at++
		}
		if at < len(a) && a[at] == "--not" {
			for _, oid := range a[at+1:] {
				if !api.ValidGitOID(oid) {
					return purgeWorkErrorV1("the fixed Git history probe changed a protected commit")
				}
			}
			return nil
		}
	}
	return purgeWorkErrorV1("the fixed reader refused another Git operation")
}

func equalPurgeReaderWordsV1(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func purgeReaderHeadRefV1(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) <= len("refs/heads/") || len(ref) > 1024 || strings.ContainsAny(ref, "\x00\r\n\\ ~^:?*[") || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return false
	}
	for _, component := range strings.Split(ref, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
		for _, char := range component {
			if char < 0x20 || char == 0x7f {
				return false
			}
		}
	}
	return true
}
