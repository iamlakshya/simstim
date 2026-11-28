package host

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// DefaultPullMaxBytes is the default cap for a git bundle created by pull
// (512 MiB, spec 6.10).
const DefaultPullMaxBytes int64 = 512 << 20

type bundleCreateArgs struct {
	Dir      string `json:"dir"`
	Branch   string `json:"branch"`
	Base     string `json:"base,omitempty"`
	Full     bool   `json:"full,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

type bundleCreateResult struct {
	Path  string `json:"path,omitempty"`
	Head  string `json:"head,omitempty"`
	Full  bool   `json:"full,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Empty bool   `json:"empty,omitempty"`
}

// bundleCreate runs inside git-op (with gitSafe: hooks and fsmonitor off, git
// 2.54 version gate, systemd scope and timeout) to pack branch into a temporary
// git bundle file under the session cache folder.
func bundleCreate(a *bundleCreateArgs) (*bundleCreateResult, error) {
	if a == nil || a.Dir == "" || a.Branch == "" {
		return nil, UserError("bad_request", "bundle.create needs dir and branch", "Update the client.")
	}
	if a.Base != "" && !isValidGitOID(a.Base) {
		return nil, UserError("bad_request", "invalid base commit ID", "Update the client.")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, UserError("no_git", "git is not installed on this host", "Install git on the host.")
	}
	if err := gitVersionGate(); err != nil {
		return nil, err
	}
	if _, err := git(a.Dir, "check-ref-format", "--branch", a.Branch); err != nil {
		return nil, UserError("bad_request", "invalid branch name", "Check the branch in the session.")
	}
	ref := "refs/heads/" + a.Branch
	head, err := git(a.Dir, "rev-parse", "--verify", ref)
	if err != nil {
		return nil, asUserError(err, "git", "Check the branch in the session.")
	}
	maxBytes := a.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultPullMaxBytes
	}

	full := a.Full || a.Base == ""
	if !full {
		if a.Base == head {
			return &bundleCreateResult{Head: head, Empty: true}, nil
		}
		// Only make an incremental bundle if base exists as an ancestor here.
		if _, err := git(a.Dir, "merge-base", "--is-ancestor", a.Base, ref); err == nil {
			cnt, cerr := git(a.Dir, "rev-list", "--count", a.Base+".."+ref)
			if cerr == nil && strings.TrimSpace(cnt) == "0" {
				return &bundleCreateResult{Head: head, Empty: true}, nil
			}
		} else {
			full = true
		}
	}

	tmpDir := filepath.Join(paths.RuntimeDir(), "bundles")
	if err := paths.EnsurePrivateDir(tmpDir); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(tmpDir, "pull-*.bundle")
	if err != nil {
		return nil, fmt.Errorf("create bundle file: %w", err)
	}
	tmpPath := f.Name()
	_ = f.Close()

	args := []string{"bundle", "create", tmpPath, ref}
	if !full {
		args = append(args, "^"+a.Base)
	}
	if _, err := git(a.Dir, args...); err != nil {
		_ = os.Remove(tmpPath)
		return nil, asUserError(err, "git", "Check the repository on the host with git status.")
	}
	st, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("stat bundle: %w", err)
	}
	if st.Size() > maxBytes {
		_ = os.Remove(tmpPath)
		return nil, UserError("pull_too_big",
			fmt.Sprintf("the git bundle is %s, above the %s limit", formatBytes(st.Size()), formatBytes(maxBytes)),
			"Push the branch to a shared remote, or raise the pull limit.")
	}
	return &bundleCreateResult{
		Path: tmpPath,
		Head: head,
		Full: full,
		Size: st.Size(),
	}, nil
}

// RunPullBundle streams a git bundle of session id's repository to out after a
// one-line JSON PullBundleHeader. Both repo.info and bundle.create run through
// git-op so hooks, fsmonitor and timeouts follow spec 19.3.
func RunPullBundle(id string, in io.Reader, out io.Writer) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	writeErr := func(err error) error {
		pe := asProtoError(err)
		hdr := api.PullBundleHeader{
			Error: &struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Hint    string `json:"hint,omitempty"`
			}{Code: pe.Code, Message: pe.Message, Hint: pe.Hint},
		}
		b, _ := json.Marshal(hdr)
		_, _ = out.Write(append(b, '\n'))
		return nil
	}
	if !validID(id) {
		return writeErr(UserError("not_found", "no session "+id, "List sessions to find its ID."))
	}
	m, err := LoadManifest(id)
	if err != nil {
		return writeErr(UserError("not_found", "no session "+id, "List sessions to find its ID."))
	}
	var req api.PullBundleArgs
	if err := json.NewDecoder(in).Decode(&req); err != nil && err != io.EOF {
		return writeErr(UserError("bad_request", "pull-bundle could not read its request: "+err.Error(), "Update the client."))
	}
	repoDir, err := resolvePullRepoDir(m, req.Repo)
	if err != nil {
		return writeErr(err)
	}
	infoAns, err := gitOp(gitOpRepoInfo, gitOpRequest{RepoInfoArgs: api.RepoInfoArgs{Dir: repoDir, Changes: true}})
	if err != nil {
		return writeErr(err)
	}
	if infoAns.Repo == nil {
		return writeErr(UserError("git", "could not inspect the repository", gitOpHint))
	}
	if infoAns.Repo.Branch == "HEAD" {
		return writeErr(UserError("detached_head", m.Name+" has no branch checked out (a detached HEAD)", "Create a branch in the session, then pull again."))
	}
	bAns, err := gitOp(gitOpBundleCreate, gitOpRequest{
		Bundle: &bundleCreateArgs{
			Dir:      infoAns.Repo.Root,
			Branch:   infoAns.Repo.Branch,
			Base:     req.Base,
			Full:     req.Full,
			MaxBytes: req.MaxBytes,
		},
	})
	if err != nil {
		return writeErr(err)
	}
	if bAns.Bundle == nil {
		return writeErr(UserError("git", "bundle creation returned no result", gitOpHint))
	}
	if bAns.Bundle.Path != "" {
		defer os.Remove(bAns.Bundle.Path)
	}
	hdr := api.PullBundleHeader{
		Repo:  infoAns.Repo,
		Head:  bAns.Bundle.Head,
		Full:  bAns.Bundle.Full,
		Size:  bAns.Bundle.Size,
		Empty: bAns.Bundle.Empty,
	}
	b, err := json.Marshal(hdr)
	if err != nil {
		return fmt.Errorf("encode pull-bundle header: %w", err)
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write pull-bundle header: %w", err)
	}
	if hdr.Empty || bAns.Bundle.Path == "" {
		return nil
	}
	f, err := os.Open(bAns.Bundle.Path)
	if err != nil {
		return fmt.Errorf("open bundle file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(out, io.LimitReader(f, hdr.Size)); err != nil {
		return fmt.Errorf("stream bundle file: %w", err)
	}
	return nil
}

func isValidGitOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return strconv.FormatInt(n, 10) + " bytes"
	}
}
