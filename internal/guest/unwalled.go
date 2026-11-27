package guest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

type unwalledGuestV1 struct {
	id, home   string
	runtime    *os.Root
	ref        cachefs.Reference
	cleanupErr error
	helper     *os.File
}

func newUnwalledGuestV1(id string) (_ *unwalledGuestV1, returned error) {
	if !api.ValidSessionID(id) {
		return nil, execError("the unwalled init needs its original primary session ID")
	}
	helper, err := adoptUnwalledExecutableV1()
	if err != nil {
		return nil, err
	}
	defer func() {
		if returned != nil && helper != nil {
			returned = errors.Join(returned, helper.Close())
		}
	}()
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil, execError("the unwalled init has no absolute host home")
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return nil, execError("the unwalled account's home is unavailable")
	}
	runtime, err := cachefs.OpenPrivateRoot(paths.SandboxRoot(id), false)
	if err != nil {
		return nil, err
	}
	ref, err := cachefs.CaptureRoot(runtime)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	return &unwalledGuestV1{id: id, home: home, runtime: runtime, ref: ref, helper: helper}, nil
}

func (g *Init) memberFilesV1(id string) host.GuestHolder {
	if g.vm != nil {
		return g.vm.holderFiles(id)
	}
	if g.unwalled != nil {
		run := g.unwalled.runtime.Name()
		return host.GuestHolder{Sock: filepath.Join(run, "holder-"+id+".sock"), Hook: filepath.Join(run, "hook-"+id+".sock"), Manifest: filepath.Join(run, "members", id, "holder.json")}
	}
	return FilesFor(g.root, id)
}

func (g *Init) projectUnwalledV1(a *proto.CtlStartArgs, primary bool) error {
	mode := g.unwalled
	if mode == nil || mode.runtime == nil || !api.ValidSessionID(a.Session) || primary && a.Session != mode.id {
		return execError("the unwalled member has no original primary and runtime")
	}
	if err := mode.ref.CheckRoot(mode.runtime); err != nil {
		return err
	}
	if a.Proxy != "" || a.CACert != "" || a.DNS != "" || a.PackageManagers != nil || a.Decoys != nil || len(a.StateFiles) != 0 || a.CaptureNonce != "" || a.InputNonce != "" || a.InputDigest != "" || a.SSHAgent {
		return execError("an unwalled start cannot install sandbox trust, network, state or signer controls")
	}
	for name, mode := range a.SecretModes {
		if mode != api.SecretModeExposed {
			return execError("an unwalled member cannot receive a bound secret")
		}
		if _, ok := a.Secrets[name]; !ok {
			return execError("the unwalled exposure declaration has no actual value")
		}
	}
	if a.DefaultShell {
		if len(a.Argv) == 0 {
			return execError("the unwalled shell command is missing")
		}
		shell := a.Shell
		if shell == "" {
			shell = host.LoginShell()
		}
		if !filepath.IsAbs(shell) {
			return execError("the unwalled login shell is not absolute")
		}
		a.Argv = slices.Clone(a.Argv)
		a.Argv[0] = shell
	}
	if !filepath.IsAbs(a.Cwd) {
		return execError("the unwalled working directory is not absolute")
	}
	files := g.memberFilesV1(a.Session)
	key := brand.Env("HOOK_SOCK") + "="
	env := make([]string, 0, len(a.Env)+1)
	for _, entry := range a.Env {
		if !strings.HasPrefix(entry, key) {
			env = append(env, entry)
		}
	}
	a.Env = append(env, key+files.Hook)
	g.mu.Lock()
	if primary {
		g.execEnv = slices.Clone(a.Env)
		g.execCwd = a.Cwd
		g.sessionPrepared = true
	}
	g.mu.Unlock()
	return nil
}

func (g *Init) unwalledPutV1(ctx context.Context, stream *link.Stream, header proto.FilePutHeaderV1) error {
	if header.Path != "" || header.Directory || header.Replace {
		return &proto.Error{Code: "cp_unwalled", Message: "an unwalled session uses the host's own files", Hint: "Use the host's ordinary file transfer command for a chosen path."}
	}
	_, relative, err := putTarget(header)
	if err != nil {
		return err
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(g.unwalled.id), false)
	if err != nil {
		return err
	}
	defer root.Close()
	paste, err := cachefs.OpenPrivateChild(root, "paste", true)
	if err != nil {
		return err
	}
	defer paste.Close()
	if err := filecopy.ReceiveFile(ctx, paste, relative, header.Size, header.SHA256, false, stream); err != nil {
		return err
	}
	g.putReply(stream, filepath.Join(paste.Name(), relative), nil)
	return nil
}

func (g *Init) unwalledProcessV1() (*syscall.SysProcAttr, error) {
	if g.unwalled == nil {
		return nil, execError("the unwalled process has no explicit host-account mode")
	}
	return unwalledProcessAttributesV1(), nil
}
