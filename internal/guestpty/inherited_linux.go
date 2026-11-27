//go:build linux

package guestpty

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// AdoptHolder validates the fixed900account and init-created Unix capability.
// An environment variable alone cannot activate the stronger guest ABI.
func AdoptHolder(session string) (*Terminal, error) {
	if os.Geteuid() != 900 || os.Getegid() != 900 || os.Getenv(HolderFDEnv) != "4" {
		return nil, fmt.Errorf("v2 holder needs its fixed900identity and inherited stream")
	}
	file := os.NewFile(4, "init-owned-pty")
	if err := validateInitStream(file); err != nil {
		file.Close()
		return nil, err
	}
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, err
	}
	channel, err := New(conn, Holder, session, os.Getenv(HolderTokenEnv))
	if err != nil {
		conn.Close()
		return nil, err
	}
	terminal, err := NewTerminal(channel)
	if err != nil {
		channel.Close()
		return nil, err
	}
	return terminal, nil
}

func validateInitStream(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var checked error
	err = raw.Control(func(fd uintptr) {
		kind, e := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		if e != nil || kind != unix.SOCK_STREAM {
			checked = fmt.Errorf("inherited PTY is not a stream socket")
			return
		}
		address, e := unix.Getsockname(int(fd))
		if e != nil {
			checked = e
			return
		}
		if _, ok := address.(*unix.SockaddrUnix); !ok {
			checked = fmt.Errorf("inherited PTY is not Unix")
			return
		}
		peer, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil || peer.Uid != 0 || peer.Pid != 1 {
			checked = fmt.Errorf("inherited PTY did not originate in actual rootinit")
			return
		}
		unix.CloseOnExec(int(fd))
	})
	if err != nil {
		return err
	}
	return checked
}

// AdoptHook validates only init's fixed listener descriptor and exact guest
// hook path. It confers no command or child ownership to the holder.
func AdoptHook(expected string) (net.Listener, error) {
	if os.Geteuid() != 900 || os.Getenv(HookFDEnv) != "5" {
		return nil, fmt.Errorf("v2 holder has no init-bound hook capability")
	}
	file := os.NewFile(5, "agent-hook-listener")
	raw, err := file.SyscallConn()
	if err != nil {
		file.Close()
		return nil, err
	}
	var checked error
	err = raw.Control(func(fd uintptr) {
		listening, e := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
		if e != nil || listening != 1 {
			checked = fmt.Errorf("inherited hook is not listening")
			return
		}
		address, e := unix.Getsockname(int(fd))
		unixAddress, ok := address.(*unix.SockaddrUnix)
		if e != nil || !ok || unixAddress.Name != expected {
			checked = fmt.Errorf("inherited hook listener names another guest path")
			return
		}
		unix.CloseOnExec(int(fd))
	})
	if err != nil || checked != nil {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, checked
	}
	listener, err := net.FileListener(file)
	file.Close()
	return listener, err
}
