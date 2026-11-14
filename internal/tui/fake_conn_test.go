package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type fakeConn struct {
	hostID    string
	idChanged bool
	be        *fakeBackend
	name      string
	done      chan struct{}
	mu        sync.Mutex
	waits     int64
	closed    bool
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *fakeConn) host() *fakeHost {
	return c.be.state[c.name]
}

func (c *fakeConn) Folders(_ context.Context, prefix string) (client.FoldersResult, error) {
	all := []string{"~/src/api/", "~/src/app/", "~/notes/"}
	c.be.mu.Lock()
	all = append(all, c.host().folders...)
	c.be.mu.Unlock()
	r := client.FoldersResult{Recent: []string{"~/src/app"}, Repos: []string{"~/src/api", "~/src/app"}}
	for _, p := range all {
		if prefix != "" && strings.HasPrefix(p, prefix) {
			r.Matches = append(r.Matches, p)
		}
	}
	return r, nil
}

func (c *fakeConn) Sessions(context.Context) ([]client.Session, error) {
	if c.isClosed() {
		return nil, client.ErrClosed
	}
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	return append([]client.Session(nil), c.host().sessions...), nil
}

func (c *fakeConn) NewSession(_ context.Context, a client.NewArgs) (*client.Session, error) {
	for k := range a.Secrets {
		c.be.record("new %s secret %s=%v", c.name, k, a.Secrets[k])
	}
	if len(a.Argv) > 0 {
		c.be.record("new %s agent=%s argv=%v cwd=%s name=%s worktree=%v size=%dx%d", c.name, a.Agent, a.Argv, a.Cwd, a.Name, a.Worktree, a.Cols, a.Rows)
	} else {
		c.be.record("new %s agent=%s cwd=%s name=%s worktree=%v size=%dx%d", c.name, a.Agent, a.Cwd, a.Name, a.Worktree, a.Cols, a.Rows)
	}
	if a.Setup != "" {
		c.be.record("new %s setup=%s", c.name, a.Setup)
	}
	if len(a.SetupHashes) > 0 {
		c.be.record("new %s setup_hashes=%v", c.name, a.SetupHashes)
	}
	if a.MakeFolder {
		c.be.record("new %s mkdir %s", c.name, a.Cwd)
	}
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	c.be.newArgs = append(c.be.newArgs, a)
	h := c.host()
	if h.newErr != nil {
		return nil, h.newErr
	}
	if h.missing != "" && a.Cwd == h.missing && !a.MakeFolder {
		data, _ := json.Marshal(client.FolderQuestion{Folder: "/home/dev/" + strings.TrimPrefix(h.missing, "~/")})
		return nil, &client.UserError{Msg: "the folder does not exist", Code: api.CodeNoFolder, Data: data}
	}
	// The order of the host: the override, a refusal, the trusted hash.
	run, known := a.SetupTrust[h.setupRepo]
	refused := known && !run
	if h.setupRepo != "" && a.Setup == "" && !refused && (h.setupHash == "" || a.SetupHashes[h.setupRepo] != h.setupHash) {
		data, _ := json.Marshal(client.SetupQuestion{Repo: h.setupRepo, Script: h.setupRepo + "/.setup", Hash: h.setupHash})
		return nil, &client.UserError{Msg: "the repository has a setup script", Code: api.CodeSetupTrust, Data: data}
	}
	if h.setupErr != "" && a.Setup != api.SetupSkip {
		return nil, &client.UserError{Msg: "the setup script of /home/dev/src/app, .x/setup, points outside the repository, so it cannot run",
			Hint: "Make .x/setup a file in the repository, or start with --no-setup-script.", Code: h.setupErr}
	}
	name := a.Name
	if name == "" {
		name = "app"
	}
	s := client.Session{ID: fmt.Sprintf("n%03d", len(h.sessions)+1), Name: name, Agent: a.Agent, Cwd: a.Cwd,
		State: api.StateStarting, Since: now, CreatedAt: now, Alive: true}
	h.sessions = append(h.sessions, s)
	return &s, nil
}

// Resume starts a session's agent again: it works once more, with nothing
// of how it ended.
func (c *fakeConn) Resume(_ context.Context, a client.ResumeArgs) (*client.ResumeResult, error) {
	c.be.record("resume %s %s memory=%d secrets=%v", c.name, a.ID, a.MemoryMax, a.Secrets)
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	h := c.host()
	if h.resumeErr != nil {
		return nil, h.resumeErr
	}
	for i, s := range h.sessions {
		if s.ID == a.ID {
			s.State, s.Source, s.Since, s.Alive = api.StateWorking, "start", now, true
			s.Reason, s.Signal, s.ExitCode, s.OOM = "", "", nil, false
			h.sessions[i] = s
			return &client.ResumeResult{Session: s, Why: "resumed its own session"}, nil
		}
	}
	return nil, &client.UserError{Msg: "no session " + a.ID, Hint: "List sessions to find its ID."}
}

// DeleteNamed deletes as the host does: only while the session's name is
// still name.
func (c *fakeConn) DeleteNamed(_ context.Context, id, name string) (client.DeleteResult, error) {
	c.be.record("delete %s %s", c.name, id)
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	h := c.host()
	for i, s := range h.sessions {
		if s.ID == id && s.Name != name {
			return client.DeleteResult{}, &client.UserError{Msg: "the session " + id + " is now named " + s.Name, Hint: "List the sessions, then delete the one you mean.", Code: "session_renamed"}
		}
		if s.ID == id {
			h.sessions = append(h.sessions[:i], h.sessions[i+1:]...)
			res := client.DeleteResult{Deleted: true}
			if s.Name == "api-tests" {
				res.Worktree = "kept the worktree ~/src/api-tests: it has uncommitted changes"
			}
			if c.be.deleted != nil {
				res = *c.be.deleted
			}
			return res, nil
		}
	}
	return client.DeleteResult{}, &client.UserError{Msg: "no session " + id, Hint: "Refresh the list."}
}

func (c *fakeConn) RenameSession(_ context.Context, id, name string) error {
	c.be.record("rename %s %s %s", c.name, id, name)
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	for i := range c.host().sessions {
		if c.host().sessions[i].ID == id {
			c.host().sessions[i].Name = name
		}
	}
	return nil
}

func (c *fakeConn) Doctor(ctx context.Context) (client.HostInfo, error) {
	c.be.mu.Lock()
	hook := c.be.doctorHook
	c.be.mu.Unlock()
	if hook != nil {
		hook(ctx, c.name)
	}
	if err := ctx.Err(); err != nil {
		return client.HostInfo{}, err
	}
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	return c.host().info, nil
}

func (c *fakeConn) Peek(_ context.Context, id string, _ int) ([]byte, int, int, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	b, ok := c.host().screens[id]
	if !ok {
		return nil, 0, 0, errors.New("no screen kept")
	}
	return b, 100, 30, nil
}

func (c *fakeConn) Setup(context.Context) (client.SetupResult, error) {
	return client.SetupResult{Steps: []api.Check{
		{Name: "linger", OK: true, Detail: "on"},
		{Name: "terminfo", OK: true, Detail: "xterm-ghostty installed"},
	}}, nil
}

func (c *fakeConn) RoundTrip(context.Context) (time.Duration, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	h := c.host()
	h.pings++
	if len(h.rtts) > 0 {
		return h.rtts[(h.pings-1)%len(h.rtts)], nil
	}
	return h.latency, nil
}

// Done counts its callers as blocked, so the harness knows they wait.
func (c *fakeConn) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.waits++
		c.be.blocked.Add(1)
	}
	return c.done
}

func (c *fakeConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.be.blocked.Add(-c.waits)
		close(c.done)
	}
}
