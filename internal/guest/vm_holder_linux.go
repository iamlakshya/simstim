//go:build linux

package guest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/guestpty"
	"github.com/iamlakshya/simstim/internal/host"
)

func (v *vmRuntime) holderFiles(id string) host.GuestHolder {
	base := filepath.Join("/run", brand.Name, "members", id)
	return host.GuestHolder{Sock: filepath.Join(base, "holder.sock"), Hook: filepath.Join(base, "hook.sock"), Manifest: filepath.Join(base, "holder.json")}
}

func (v *vmRuntime) configureHolder(command *exec.Cmd, id, token string, files host.GuestHolder) (func(), error) {
	if len(command.ExtraFiles) != 1 {
		return nil, fmt.Errorf("VM holder expected exactly its canonical event descriptor")
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// paths.EnsurePrivateDir may have made this parent0700 while writing
	// the fresh manifest. Only root may list/change it; both fixed guest
	// accounts need traversal to their distinct socket capabilities.
	if err := vmOwnedDirectory(root, filepath.Join("run", brand.Name, "members"), 0o711, 0, 0, v.files.ops.own); err != nil {
		return nil, err
	}
	base := filepath.Join("run", brand.Name, "members", id)
	if err := vmOwnedDirectory(root, base, 0o711, holderUID, holderGID, v.files.ops.own); err != nil {
		return nil, err
	}
	holderHome := filepath.Join(base, "home")
	if err := vmOwnedDirectory(root, holderHome, 0o700, holderUID, holderGID, v.files.ops.own); err != nil {
		return nil, err
	}
	command.Env = holderEnv(command.Env, "/"+holderHome)
	manifest, err := root.Open(filepath.Join(base, "holder.json"))
	if err != nil {
		return nil, err
	}
	err = v.files.ops.own(manifest, holderUID, holderGID)
	closeErr := manifest.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	ours, theirs, err := SocketPair()
	if err != nil {
		return nil, err
	}
	channel, err := guestpty.New(ours, guestpty.Init, id, token)
	if err != nil {
		ours.Close()
		theirs.Close()
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: files.Hook, Net: "unix"})
	if err != nil {
		channel.Close()
		theirs.Close()
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chown(files.Hook, AgentUID, AgentGID); err != nil {
		listener.Close()
		channel.Close()
		theirs.Close()
		return nil, err
	}
	if err := os.Chmod(files.Hook, 0o600); err != nil {
		listener.Close()
		channel.Close()
		theirs.Close()
		return nil, err
	}
	hook, err := listener.File()
	listener.Close()
	if err != nil {
		channel.Close()
		theirs.Close()
		return nil, err
	}
	terminal := &vmPTY{session: id, token: token, channel: channel, done: make(chan struct{}), outputDone: make(chan struct{}), inputDone: make(chan struct{})}
	v.mu.Lock()
	if v.ptys[id] != nil {
		v.mu.Unlock()
		hook.Close()
		theirs.Close()
		channel.Close()
		return nil, vmError("the VM member PTY already exists")
	}
	v.ptys[id] = terminal
	v.mu.Unlock()
	command.ExtraFiles = append(command.ExtraFiles, theirs, hook)
	command.Env = append(command.Env, guestpty.HolderFDEnv+"=4", guestpty.HolderTokenEnv+"="+token, guestpty.HookFDEnv+"=5")
	return func() {
		theirs.Close()
		hook.Close()
		if command.Process == nil {
			channel.Close()
			v.mu.Lock()
			delete(v.ptys, id)
			v.mu.Unlock()
		}
	}, nil
}

func (v *vmRuntime) startHolderCommand(command *exec.Cmd) error {
	if v.memory == nil {
		return vmError("the VM holder has no guest memory policy")
	}
	err := v.memory.holderStart(command.Start)
	if err != nil && command.Process != nil {
		// A successful Start MUST enter the existing holder registry/waiter,
		// even when restoring PID1's priority failed. end waits on Init.mu
		// until startHolder has installed that owned process handle.
		v.setError(err)
		go v.init.end()
		return nil
	}
	return err
}
