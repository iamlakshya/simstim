package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	maxKeepFileSize    = 32 << 20 // 32 MiB per kept file
	pendingExecTTL     = 30 * time.Second
	maxPendingExecs    = 64
	defaultKillGraceMS = 3000
)

var keepSidecars = []string{"", "-wal", "-shm", "-journal"}

func (r *runner) handleMoreCtl(ctx context.Context, reply func(any, *proto.Error), req proto.Request) {
	switch req.Op {
	case "spawn":
		var a SpawnReq
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, &proto.Error{Code: "bad_request", Message: "invalid spawn request", Hint: "Update the host helper."})
			return
		}
		res, perr := r.spawnHolder(ctx, a)
		reply(res, perr)
	case "kill_session":
		var a KillSessionReq
		if err := json.Unmarshal(req.Args, &a); err != nil || a.Session == "" {
			reply(nil, &proto.Error{Code: "bad_request", Message: "invalid kill_session request", Hint: "Pass a session id."})
			return
		}
		r.killHolder(a)
		reply(map[string]any{}, nil)
	case "exec":
		var a ExecReq
		if err := json.Unmarshal(req.Args, &a); err != nil {
			reply(nil, &proto.Error{Code: "bad_request", Message: "invalid exec request", Hint: "Update the host helper."})
			return
		}
		if perr := r.registerExec(a); perr != nil {
			reply(nil, perr)
			return
		}
		reply(map[string]any{}, nil)
	default:
		reply(nil, &proto.Error{Code: "unknown_op", Message: "unknown ctl op " + req.Op, Hint: "Update the host helper."})
	}
}

func (r *runner) spawnHolder(ctx context.Context, a SpawnReq) (*SpawnResp, *proto.Error) {
	if a.Session == "" || len(a.Argv) == 0 {
		return nil, &proto.Error{Code: "bad_request", Message: "spawn requires session and argv", Hint: "Update the host helper."}
	}
	cwd := a.Cwd
	if cwd == "" {
		r.mu.Lock()
		cwd = r.baseCwd
		r.mu.Unlock()
	}
	res, perr := r.startHolder(ctx, StartReq{
		Session:  a.Session,
		Name:     a.Name,
		Agent:    a.Agent,
		Argv:     a.Argv,
		Env:      a.Env,
		Secrets:  a.Secrets,
		Cwd:      cwd,
		Term:     a.Term,
		Cols:     a.Cols,
		Rows:     a.Rows,
		RunHook:  a.RunHook,
		Notes:    a.Notes,
		AgentID:  a.AgentID,
		IDSource: a.IDSource,
	})
	if perr != nil {
		return nil, perr
	}
	return &SpawnResp{
		PID:       res.AgentPID,
		HolderPID: res.HolderPID,
		Cols:      res.Cols,
		Rows:      res.Rows,
	}, nil
}

func (r *runner) killHolder(a KillSessionReq) {
	r.mu.Lock()
	mh := r.holders[a.Session]
	delete(r.holders, a.Session)
	r.mu.Unlock()
	if mh == nil {
		return
	}
	grace := time.Duration(a.GraceMS) * time.Millisecond
	if grace <= 0 {
		grace = defaultKillGraceMS * time.Millisecond
	}
	if mh.cmd != nil && mh.cmd.Process != nil {
		_ = mh.cmd.Process.Signal(syscall.SIGTERM)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-mh.done:
	case <-timer.C:
		if mh.cmd != nil && mh.cmd.Process != nil {
			_ = mh.cmd.Process.Kill()
		}
		if mh.agentPID > 1 && r.opts.MicroVM {
			killAgentGroup(mh.agentPID)
		}
		<-mh.done
	}
}

func (r *runner) prepareSessionEnv(a StartReq, first bool) ([]string, *proto.Error) {
	if first {
		agentEnv, err := applyStartPackageSettings(a, a.Env)
		if err != nil {
			return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check sandbox.package_managers and network.min_release_age."}
		}
		agentEnv = applyStartCaches(a, agentEnv)
		agentEnv, err = applyStartGateway(a, agentEnv)
		if err != nil {
			return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check the agent's model gateway configuration."}
		}
		if r.opts.MicroVM {
			agentEnv = ensureAgentsPath(agentEnv, r.agentsBinDir())
		}
		agentEnv = withEnvVar(agentEnv, "BROWSER", BrowserHelperPath(r.runDir))
		base := stripSessionEnv(agentEnv)
		r.mu.Lock()
		r.baseEnv = base
		r.baseCwd = a.Cwd
		r.keepFiles = append([]string(nil), a.Keep...)
		r.mu.Unlock()
		r.restoreKeepFiles()
		if r.opts.MicroVM {
			chownPathForAgent(envLookup(agentEnv, "HOME"))
		}
		return agentEnv, nil
	}
	r.mu.Lock()
	base := append([]string(nil), r.baseEnv...)
	r.mu.Unlock()
	agentEnv := mergeEnvSlices(base, a.Env)
	agentEnv = applyStartCaches(a, agentEnv)
	agentEnv, err := applyStartGateway(a, agentEnv)
	if err != nil {
		return nil, &proto.Error{Code: "start_failed", Message: err.Error(), Hint: "Check the agent's model gateway configuration."}
	}
	if r.opts.MicroVM {
		agentEnv = ensureAgentsPath(agentEnv, r.agentsBinDir())
	}
	agentEnv = withEnvVar(agentEnv, "BROWSER", BrowserHelperPath(r.runDir))
	return agentEnv, nil
}

func (r *runner) execBaseEnv(extra []string) []string {
	r.mu.Lock()
	base := append([]string(nil), r.baseEnv...)
	r.mu.Unlock()
	if len(base) == 0 {
		base = os.Environ()
	}
	return mergeEnvSlices(base, extra)
}

func stripSessionEnv(env []string) []string {
	hookKey := brand.Env("HOOK_SOCK") + "="
	sessKey := brand.Env("SESSION") + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, hookKey) || strings.HasPrefix(kv, sessKey) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func mergeEnvSlices(base, overrides []string) []string {
	out := append([]string(nil), base...)
	for _, kv := range overrides {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		out = withEnvVar(out, k, v)
	}
	return out
}

func guestHomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "/home/agent"
}

// restoreKeepFiles copies named [sandbox] keep files from <runDir>/keep into
// the guest home folder without following symlinks (spec 6.11).
func (r *runner) restoreKeepFiles() {
	r.mu.Lock()
	files := append([]string(nil), r.keepFiles...)
	r.mu.Unlock()
	if len(files) == 0 {
		return
	}
	keepDir := filepath.Join(r.runDir, "keep")
	srcRoot, err := os.OpenRoot(keepDir)
	if err != nil {
		return
	}
	defer srcRoot.Close()
	dstRoot, err := os.OpenRoot(guestHomeDir())
	if err != nil {
		return
	}
	defer dstRoot.Close()
	for _, rel := range files {
		clean, err := api.CheckKeepPath(rel)
		if err != nil {
			continue
		}
		for _, suffix := range keepSidecars {
			_ = copyRegularUnderRoots(srcRoot, dstRoot, clean+suffix)
		}
	}
}

// saveKeepFiles persists named [sandbox] keep files from the guest home folder
// back into <runDir>/keep on clean sandbox stop without following symlinks.
func (r *runner) saveKeepFiles() {
	r.mu.Lock()
	files := append([]string(nil), r.keepFiles...)
	r.mu.Unlock()
	if len(files) == 0 {
		return
	}
	keepDir := filepath.Join(r.runDir, "keep")
	if _, err := os.Stat(keepDir); err != nil {
		return
	}
	srcRoot, err := os.OpenRoot(guestHomeDir())
	if err != nil {
		return
	}
	defer srcRoot.Close()
	dstRoot, err := os.OpenRoot(keepDir)
	if err != nil {
		return
	}
	defer dstRoot.Close()
	for _, rel := range files {
		clean, err := api.CheckKeepPath(rel)
		if err != nil {
			continue
		}
		for _, suffix := range keepSidecars {
			_ = copyRegularUnderRoots(srcRoot, dstRoot, clean+suffix)
		}
	}
}

func copyRegularUnderRoots(srcRoot, dstRoot *os.Root, rel string) error {
	fi, err := srcRoot.Lstat(rel)
	if err != nil {
		return fmt.Errorf("stat kept file %s: %w", rel, err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxKeepFileSize {
		return nil
	}
	src, err := srcRoot.Open(rel)
	if err != nil {
		return fmt.Errorf("open kept file %s: %w", rel, err)
	}
	defer src.Close()
	if dir := path.Dir(rel); dir != "." && dir != "" {
		if err := mkdirAllRoot(dstRoot, dir); err != nil {
			return err
		}
	}
	// Refuse overwriting a symlink at the destination.
	if dfi, err := dstRoot.Lstat(rel); err == nil && !dfi.Mode().IsRegular() {
		return errors.New("destination is not a regular file")
	}
	dst, err := dstRoot.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create kept file %s: %w", rel, err)
	}
	_, copyErr := io.Copy(dst, io.LimitReader(src, maxKeepFileSize))
	closeErr := dst.Close()
	if copyErr != nil {
		return fmt.Errorf("copy kept file %s: %w", rel, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close kept file %s: %w", rel, closeErr)
	}
	return nil
}

func mkdirAllRoot(root *os.Root, dir string) error {
	parts := strings.Split(path.Clean(dir), "/")
	cur := ""
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if cur == "" {
			cur = p
		} else {
			cur += "/" + p
		}
		if fi, err := root.Lstat(cur); err == nil {
			if !fi.IsDir() {
				return errors.New("keep parent is not a directory")
			}
			continue
		}
		if err := root.Mkdir(cur, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("mkdir %s: %w", cur, err)
		}
	}
	return nil
}
