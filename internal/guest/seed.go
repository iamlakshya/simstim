package guest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

var guestCommit = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func (g *Init) seed(a proto.CtlSeedArgs) (proto.CtlSeedResult, error) {
	if !files.Relative(a.Repo) || strings.Contains(a.Repo, "/") || a.Size < 0 || !api.ValidSessionID(a.Token) {
		return proto.CtlSeedResult{}, fmt.Errorf("invalid seed declaration")
	}
	g.mu.Lock()
	busy := g.sessionPrepared || g.holder != nil || g.starting || g.ended
	previous := g.seedArgs
	done := g.seedDone
	g.mu.Unlock()
	if busy {
		return proto.CtlSeedResult{}, fmt.Errorf("the workspace cannot be seeded after its holder starts")
	}
	if a.Apply {
		if previous == nil || previous.Token != a.Token || done == nil {
			return proto.CtlSeedResult{}, fmt.Errorf("no matching seed stream")
		}
		select {
		case err := <-done:
			return proto.CtlSeedResult{}, err
		case <-g.done:
			return proto.CtlSeedResult{}, fmt.Errorf("sandbox ended during seed")
		}
	}
	if previous != nil {
		return proto.CtlSeedResult{}, fmt.Errorf("a seed is already in progress")
	}
	work := g.path("/work")
	if err := os.MkdirAll(work, 0700); err != nil {
		return proto.CtlSeedResult{}, err
	}
	root, err := os.OpenRoot(work)
	if err != nil {
		return proto.CtlSeedResult{}, err
	}
	defer root.Close()
	if st, err := root.Lstat(a.Repo); err == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return proto.CtlSeedResult{}, fmt.Errorf("workspace is not a directory")
		}
		return proto.CtlSeedResult{}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return proto.CtlSeedResult{}, err
	}
	if a.Scratch {
		if err := root.Mkdir(a.Repo, 0700); err != nil {
			return proto.CtlSeedResult{}, err
		}
		if g.vm != nil {
			return proto.CtlSeedResult{}, g.vm.seedDirectory(filepath.Join(work, a.Repo))
		}
		return proto.CtlSeedResult{}, nil
	}
	if !guestCommit.MatchString(a.Commit) || a.SourceBranch == "" || a.Branch == "" || a.Size == 0 {
		return proto.CtlSeedResult{}, fmt.Errorf("seed has no commit, branch or bundle")
	}
	for _, item := range a.Includes {
		if !files.Relative(item.Path) || item.Size < 0 {
			return proto.CtlSeedResult{}, fmt.Errorf("invalid included file")
		}
	}
	done = make(chan error, 1)
	g.mu.Lock()
	g.seedArgs = &a
	g.seedDone = done
	g.mu.Unlock()
	go func() {
		select {
		case st := <-g.seedStreams:
			var header struct {
				Token string `json:"token"`
			}
			err := st.Header(&header)
			if err == nil && header.Token != a.Token {
				err = fmt.Errorf("seed stream names another request")
			}
			if err == nil {
				err = g.cloneSeed(a, st)
			}
			_ = st.Close()
			done <- err
		case <-g.done:
			done <- fmt.Errorf("sandbox ended before the seed arrived")
		}
	}()
	return proto.CtlSeedResult{Needed: true}, nil
}

func (g *Init) cloneSeed(a proto.CtlSeedArgs, st *link.Stream) error {
	if g.vm != nil {
		release, err := g.vm.seedStream(st)
		if err != nil {
			return err
		}
		defer release()
	}
	temp, err := os.MkdirTemp(g.path("/tmp"), "seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if g.vm != nil {
		if err := g.vm.seedDirectory(temp); err != nil {
			return err
		}
	}
	bundle := filepath.Join(temp, "seed.bundle")
	out, err := os.OpenFile(bundle, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if g.vm != nil {
		if err := g.vm.seedFile(out); err != nil {
			out.Close()
			return err
		}
	}
	_, err = io.CopyN(out, st, a.Size)
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("receive seed bundle: %w", err)
	}
	work := g.path("/work")
	stage := filepath.Join(work, ".seed-"+a.Token)
	_ = os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	if err = g.guestGit(work, "clone", "--no-checkout", "--no-hardlinks", "--template=", "--branch", a.SourceBranch, "--", bundle, stage); err != nil {
		return err
	}
	if err = g.guestGit(stage, "checkout", "-b", a.Branch, a.Commit); err != nil {
		return err
	}
	if a.Origin != "" {
		if err = g.guestGit(stage, "remote", "set-url", "origin", a.Origin); err != nil {
			return err
		}
	} else if err = g.guestGit(stage, "remote", "remove", "origin"); err != nil {
		return err
	}
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, item := range a.Includes {
		if err = root.MkdirAll(filepath.Dir(item.Path), 0700); err != nil {
			return err
		}
		if err = files.CheckIncludeParent(root, item.Path); err != nil {
			return err
		}
		if g.vm != nil {
			current := stage
			for _, part := range strings.Split(filepath.Dir(item.Path), "/") {
				if part == "." {
					continue
				}
				current = filepath.Join(current, part)
				if err := g.vm.seedDirectory(current); err != nil {
					return err
				}
			}
		}
		// The clone is not running yet. os.Root keeps even a tracked parent
		// symlink inside this clone. Rename replaces an existing final link.
		temporary := item.Path + ".include-new"
		out, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if g.vm != nil {
			if err := g.vm.seedFile(out); err != nil {
				out.Close()
				return err
			}
		}
		_, err = io.CopyN(out, st, item.Size)
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err = root.Rename(temporary, item.Path); err != nil {
			return err
		}
	}
	var extra [1]byte
	n, err := st.Read(extra[:])
	if n != 0 || err != io.EOF {
		return fmt.Errorf("seed stream has extra or unfinished bytes")
	}
	workspace, err := os.OpenRoot(work)
	if err != nil {
		return err
	}
	defer workspace.Close()
	return workspace.Rename(filepath.Base(stage), a.Repo)
}

func (g *Init) guestGit(dir string, args ...string) error {
	if g.vm != nil {
		return g.vm.seedGit(dir, args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", host.SafeGitArgs(dir, args...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + g.path(host.GuestHome), "XDG_CONFIG_HOME=" + g.path(host.GuestHome) + "/.config", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C.UTF-8"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("guest Git failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
