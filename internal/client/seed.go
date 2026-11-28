package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// DefaultSeedMaxBytes caps one laptop git bundle created by --seed
// (512 MiB, spec 17.4).
const DefaultSeedMaxBytes int64 = 512 << 20

// PreparedSeed holds one temporary git bundle ready to upload over SFTP.
type PreparedSeed struct {
	Name       string
	Branch     string
	Origin     string
	UserName   string
	UserEmail  string
	BundlePath string
	Size       int64
	Untracked  []string
}

// PreparedSeeds holds temporary bundles for one `new --seed` invocation.
type PreparedSeeds struct {
	dir   string
	Items []PreparedSeed
}

// Cleanup removes all temporary files created by PrepareSeeds.
func (p *PreparedSeeds) Cleanup() {
	if p != nil && p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// PrepareSeeds bundles one or more laptop git repositories for `new --seed`
// (and optional `--seed-dirty`) without modifying laptop refs or index and
// without running repository clean/process filters (spec 17.4).
func PrepareSeeds(ctx context.Context, dirs []string, explicitHostDir string, dirty bool, maxBytes int64, out io.Writer) (*PreparedSeeds, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultSeedMaxBytes
	}
	if out == nil {
		out = io.Discard
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, &UserError{
			Msg:  "git is not installed on this laptop",
			Hint: "Install git, then run the command again.",
			Code: "no_git",
		}
	}
	tmpDir, err := os.MkdirTemp("", brand.Name+"-seed-*")
	if err != nil {
		return nil, fmt.Errorf("create seed temp folder: %w", err)
	}
	res := &PreparedSeeds{dir: tmpDir}
	ok := false
	defer func() {
		if !ok {
			res.Cleanup()
		}
	}()

	seenNames := map[string]string{}
	if explicitHostDir != "" && explicitHostDir != "~" {
		base := filepath.Base(strings.TrimRight(explicitHostDir, "/"))
		if base != "" && base != "." && base != "/" {
			seenNames[base] = explicitHostDir
		}
	}
	for i, rawDir := range dirs {
		item, err := prepareOneSeed(ctx, rawDir, i, tmpDir, dirty, maxBytes, out, seenNames)
		if err != nil {
			return nil, err
		}
		res.Items = append(res.Items, item)
	}
	ok = true
	return res, nil
}

func prepareOneSeed(ctx context.Context, rawDir string, idx int, tmpDir string, dirty bool, maxBytes int64, out io.Writer, seenNames map[string]string) (PreparedSeed, error) {
	abs, err := filepath.Abs(rawDir)
	if err != nil {
		return PreparedSeed{}, fmt.Errorf("resolve %s: %w", rawDir, err)
	}
	top, err := seedGitOut(ctx, abs, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return PreparedSeed{}, &UserError{
			Msg:  CleanPath(rawDir) + " is not inside a git repository",
			Hint: "Pass a git repository folder to --seed.",
			Code: "not_a_repo",
		}
	}
	top = strings.TrimSpace(top)
	name := filepath.Base(top)
	if prev, dup := seenNames[name]; dup {
		return PreparedSeed{}, &UserError{
			Msg:  fmt.Sprintf("two folders share the base name %q: %s and %s", name, CleanPath(prev), CleanPath(top)),
			Hint: "Seed repositories with distinct folder names, or omit --dir.",
			Code: "seed_conflict",
			Kind: KindUsage,
		}
	}
	seenNames[name] = top

	branch, err := seedGitOut(ctx, top, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return PreparedSeed{}, &UserError{
			Msg:  CleanPath(top) + " has no branch checked out (a detached HEAD)",
			Hint: "Check out a branch in the repository, then run --seed again.",
			Code: "detached_head",
		}
	}
	branch = strings.TrimSpace(branch)
	if _, err := seedGitOut(ctx, top, nil, "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		return PreparedSeed{}, &UserError{
			Msg:  fmt.Sprintf("branch %s in %s has no commits yet", CleanLine(branch), CleanPath(top)),
			Hint: "Make an initial commit before seeding the repository.",
			Code: "seed_failed",
		}
	}

	origin, _ := seedGitOut(ctx, top, nil, "remote", "get-url", "origin")
	userName, _ := seedGitOut(ctx, top, nil, "config", "user.name")
	userEmail, _ := seedGitOut(ctx, top, nil, "config", "user.email")

	bundlePath := filepath.Join(tmpDir, fmt.Sprintf("seed-%d.bundle", idx))
	var untracked []string
	if dirty {
		untracked, err = buildDirtySeedBundle(ctx, top, branch, strings.TrimSpace(userName), strings.TrimSpace(userEmail), tmpDir, idx, bundlePath, out)
		if err != nil {
			return PreparedSeed{}, err
		}
	} else {
		if _, err := seedGitOut(ctx, top, nil, "bundle", "create", bundlePath, "refs/heads/"+branch); err != nil {
			return PreparedSeed{}, &UserError{
				Msg:  fmt.Sprintf("could not bundle %s: %v", CleanPath(top), CleanLine(err.Error())),
				Hint: "Check the repository with git status and try again.",
				Code: "seed_failed",
			}
		}
	}
	st, err := os.Stat(bundlePath)
	if err != nil {
		return PreparedSeed{}, fmt.Errorf("stat seed bundle: %w", err)
	}
	if st.Size() > maxBytes {
		return PreparedSeed{}, &UserError{
			Msg:  fmt.Sprintf("seed bundle for %s is %s, above the %s limit", name, formatCopySize(st.Size()), formatCopySize(maxBytes)),
			Hint: "Clone the repository directly on the host, or raise [sandbox] seed_max in config.toml.",
			Code: "seed_too_big",
			Kind: KindRefused,
		}
	}
	return PreparedSeed{
		Name:       name,
		Branch:     branch,
		Origin:     strings.TrimSpace(origin),
		UserName:   strings.TrimSpace(userName),
		UserEmail:  strings.TrimSpace(userEmail),
		BundlePath: bundlePath,
		Size:       st.Size(),
		Untracked:  untracked,
	}, nil
}

// UploadSeedBundles uploads every prepared bundle into the session's staging
// directory over P1.6's pkg/sftp connection.
func UploadSeedBundles(sc *sftp.Client, seeds *PreparedSeeds) ([]api.SeedRepoSpec, error) {
	if seeds == nil || len(seeds.Items) == 0 {
		return nil, nil
	}
	if err := sc.MkdirAll("seeds"); err != nil {
		return nil, fmt.Errorf("create seeds staging folder: %w", err)
	}
	out := make([]api.SeedRepoSpec, 0, len(seeds.Items))
	for i, item := range seeds.Items {
		rel := path.Join("seeds", strconv.Itoa(i)+".bundle")
		in, err := os.Open(item.BundlePath)
		if err != nil {
			return nil, fmt.Errorf("open seed bundle %s: %w", item.Name, err)
		}
		f, err := sc.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			_ = in.Close()
			return nil, fmt.Errorf("create staged bundle %s: %w", rel, err)
		}
		_, copyErr := io.Copy(f, in)
		closeErr := f.Close()
		_ = in.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("upload seed bundle %s: %w", item.Name, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("finish seed bundle %s: %w", item.Name, closeErr)
		}
		out = append(out, api.SeedRepoSpec{
			Name:      item.Name,
			Branch:    item.Branch,
			Origin:    item.Origin,
			UserName:  item.UserName,
			UserEmail: item.UserEmail,
			BundleRel: rel,
		})
	}
	return out, nil
}

func seedGitCmd(ctx context.Context, dir string, extraEnv []string, args ...string) *exec.Cmd {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	return cmd
}

func seedGitOut(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	b, err := seedGitBytes(ctx, dir, extraEnv, args...)
	return string(b), err
}

func seedGitBytes(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := seedGitCmd(ctx, dir, extraEnv, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return out.Bytes(), nil
}

func seedGitStdin(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) error {
	cmd := seedGitCmd(ctx, dir, extraEnv, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
