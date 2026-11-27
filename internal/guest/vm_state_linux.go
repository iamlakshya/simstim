//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/files"
)

type vmState struct {
	plan    files.StatePlan
	current string
	session string
	home    string
	ready   bool
}

func (v *vmFiles) restoreFiles(plan vmPreparePlan) (*vmState, error) {
	session := v.boot("mnt/session")
	root, err := os.OpenRoot(session)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	restore := "state/files/empty"
	if nonce := plan.Args.VM.RestoreCaptureNonce; nonce != "" {
		restore = "state/files/generations/" + nonce
	}
	info, err := root.Lstat(restore)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, vmError("the host-accepted VM state generation is missing or unsafe")
	}
	opened, err := root.OpenRoot(restore)
	if err != nil {
		return nil, err
	}
	opened.Close()
	home := filepath.Join(v.root, "home/agent")
	if err := files.RestoreFamilies(home, filepath.Join(session, restore), plan.State.Files); err != nil {
		return nil, err
	}
	newRoot, err := os.OpenRoot(v.root)
	if err != nil {
		return nil, err
	}
	defer newRoot.Close()
	for _, name := range plan.State.Files {
		if parent := filepath.Dir(name); parent != "." {
			if err := v.agentPath(newRoot, parent); err != nil {
				return nil, err
			}
		}
	}
	members, err := files.FamilyMembers(plan.State.Files)
	if err != nil {
		return nil, err
	}
	homeRoot, err := newRoot.OpenRoot("home/agent")
	if err != nil {
		return nil, err
	}
	defer homeRoot.Close()
	for _, name := range members {
		file, err := files.OpenRegular(homeRoot, name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		err = v.ops.own(file, AgentUID, AgentGID)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return &vmState{plan: plan.State, current: plan.Args.CaptureNonce, session: "/.persist", home: "/home/agent"}, nil
}

// capture produces only a private PENDING generation. The guest never writes
// CURRENT or selects a committed epoch. Only the protected host's next
// RestoreCaptureNonce, after owned clean VM exit/all cleanup, can select it.
func (s *vmState) capture(clean bool) error {
	if !clean || len(s.plan.Files) == 0 {
		return nil
	}
	if !vmCaptureNonce(s.current) {
		return vmError("the VM pending state generation has no current nonce")
	}
	root, err := os.OpenRoot(s.session)
	if err != nil {
		return err
	}
	defer root.Close()
	parent, err := root.OpenRoot("state/files/generations")
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := parent.Mkdir(s.current, 0o700); err != nil {
		return vmError("the VM pending state generation already exists or cannot be made")
	}
	complete := false
	defer func() {
		if !complete {
			_ = parent.RemoveAll(s.current)
		}
	}()
	path := filepath.Join(s.session, "state/files/generations", s.current)
	if err := files.CaptureFamilies(s.home, path, s.plan.Files); err != nil {
		return err
	}
	directory, err := parent.Open(".")
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return fmt.Errorf("sync VM pending generation: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	s.ready = true
	complete = true
	return nil
}
