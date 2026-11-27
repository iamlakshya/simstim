package host

import (
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/guestpty"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

// adoptVMTerminal is a data-plane join only. Linux verifies fixed900 and the
// root PID1 peer on inherited FD4; an environment marker confers no authority.
func (h *Holder) adoptVMTerminal() error {
	if os.Getenv(guestpty.HolderFDEnv) == "" && os.Getenv(guestpty.HolderTokenEnv) == "" {
		return nil
	}
	if err := secretmem.Protect(); err != nil {
		return UserError("guest_pty", "the VM holder cannot disable its own core files and dumpability", "Update the VM helper, then resume.")
	}
	terminal, err := guestpty.AdoptHolder(h.id)
	if err != nil {
		return UserError("guest_pty", "the VM holder cannot adopt its init-owned terminal: "+err.Error(), "Start the holder through the matching VM init.")
	}
	h.vmTerm = terminal
	return nil
}

func (h *Holder) startVMTerminal() error {
	cols, rows := h.startSize()
	if _, ok := agents.Find(h.m.Agent.Kind); ok {
		h.scr = newScreenModel(cols, rows)
	}
	h.mu.Lock()
	now := time.Now().UTC()
	h.m.Run.HolderPID = os.Getpid()
	h.m.Run.AgentPID = 0 // A guest claim never becomes a borrowed PID capability.
	h.m.Run.Socket = h.guest.Sock
	h.m.Run.StartedAt = now
	h.m.Status = Status{State: StateWorking, Since: now, Source: "guest"}
	h.lastOut = time.Now()
	copy := h.copyForSave()
	h.mu.Unlock()
	return h.save(copy, false)
}

func (h *Holder) readPTY(data []byte) (int, error) {
	if h.vmTerm != nil {
		return h.vmTerm.Read(data)
	}
	return h.ptmx.Read(data)
}
func (h *Holder) writePTY(data []byte) (int, error) {
	if h.vmTerm != nil {
		return h.vmTerm.Write(data)
	}
	return h.ptmx.Write(data)
}

// stopVMTerminal sends only channel controls. Init owns the process group,
// unique cgroup, final signal, unreaped wait, and last output drain.
func (h *Holder) stopVMTerminal() {
	_ = h.vmTerm.Signal(1)
	finished := make(chan struct{})
	go func() { _, _ = h.vmTerm.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		_ = h.vmTerm.Signal(9)
		select {
		case <-finished:
		case <-time.After(time.Second):
		}
	}
	_ = h.vmTerm.Close()
}

func (h *Holder) vtPath() (string, error) {
	if h.vmTerm != nil {
		// Init installed this fixed verified read-only tools path before pivot.
		// The spelling of /proc/self/exe cannot select an image-owned VT.
		return filepath.Join("/opt", brand.Name, "bin", brand.VTBinary), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), brand.VTBinary), nil
}
