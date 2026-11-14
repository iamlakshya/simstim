package host

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (p *holderStartup) renameRequest(c *hclient, req proto.Request) {
	var args struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(req.Args, &args) != nil || strings.TrimSpace(args.Name) == "" {
		p.reply(c, req.ID, nil, &proto.Error{Code: "bad_request", Message: "a name is needed", Hint: "Pass a non-empty name for the session."})
		return
	}
	name := strings.TrimSpace(args.Name)
	err := p.rename(name)
	var perr *proto.Error
	switch {
	case err == nil:
		p.reply(c, req.ID, map[string]string{"name": name}, nil)
	case errors.As(err, &perr):
		p.reply(c, req.ID, nil, perr)
	default:
		p.reply(c, req.ID, nil, &proto.Error{Code: "save", Message: "cannot save the new name: " + err.Error(), Hint: "Check the host's disk with the host doctor."})
	}
}

// rename follows the same operation/first-save order as a stopped rename.
// The final pending gate makes the save precede an accepted stop or launch.
func (p *holderStartup) rename(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	unlock, err := lockFileWaitContext(p.ctx, p.peer.id, lockFile, renameWait)
	if err != nil {
		return err
	}
	defer unlock()
	unlockFirst, err := lockFileWaitContext(p.ctx, p.peer.id, firstLockFile, renameWait)
	if err != nil {
		return err
	}
	defer unlockFirst()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.launching || p.active != nil || p.ctx.Err() != nil {
		return pendingHolderError()
	}
	m, err := LoadManifest(p.peer.id)
	if err != nil {
		return err
	}
	if m.Status.State != StateStarting || m.Run.AgentPID != 0 {
		return pendingHolderError()
	}
	m.Name = name
	if err := m.Save(); err != nil {
		return err
	}
	_ = os.Remove(unsavedPath(p.peer.id))
	return nil
}
