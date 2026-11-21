//go:build linux

package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func denied(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENETUNREACH)
}

func socketDenied(family, kind, protocol int) bool {
	fd, err := unix.Socket(family, kind|unix.SOCK_CLOEXEC, protocol)
	if err == nil {
		_ = unix.Close(fd)
	}
	return denied(err)
}

func pathDenied(name string) bool {
	file, err := os.Open(name)
	if err == nil {
		_ = file.Close()
	}
	return denied(err)
}

func unixDenied(name string) bool {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return denied(err)
	}
	defer unix.Close(fd)
	return denied(unix.Connect(fd, &unix.SockaddrUnix{Name: name}))
}

func escapeChecks(args []string) map[string]bool {
	if len(args) != 5 {
		return map[string]bool{"arguments": false}
	}
	for index, value := range args {
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return map[string]bool{"arguments": false}
		}
		args[index] = string(decoded)
	}
	marker, bus, other, hostPIDNamespace := args[0], args[1], args[2], args[3]
	hostBootID := args[4]
	checks := map[string]bool{"uid_1000": os.Getuid() == 1000, "host_home": pathDenied(marker), "host_bus": unixDenied(bus), "other_session_socket": unixDenied(other), "no_kvm": pathDenied("/dev/kvm"), "no_vsock": socketDenied(unix.AF_VSOCK, unix.SOCK_STREAM, 0), "no_audit": socketDenied(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_AUDIT)}
	_, _, err := unix.Syscall(unix.SYS_IO_URING_SETUP, 1, 0, 0)
	checks["no_io_uring"] = denied(err)
	_, _, err = unix.Syscall(unix.SYS_CLONE3, 0, 0, 0)
	checks["clone3_enosys"] = errors.Is(err, unix.ENOSYS)
	checks["no_nested_userns"] = denied(unix.Unshare(unix.CLONE_NEWUSER))
	checks["no_terminal_injection"] = denied(unix.IoctlSetInt(0, unix.TIOCSTI, int('x')))
	checks["route_newaddr_eperm"] = routeDenied()
	namespace, namespaceErr := os.Readlink("/proc/self/ns/pid")
	boot, bootErr := os.ReadFile("/proc/sys/kernel/random/boot_id")
	initLine, initErr := os.ReadFile("/proc/1/cmdline")
	// Initial namespace inode numbers can coincide across two kernels.
	// A VM's different boot and its actual guest init distinguish that case;
	// a process wall instead has another PID namespace in the same kernel.
	separate := namespace != hostPIDNamespace || bootErr == nil && strings.TrimSpace(string(boot)) != hostBootID
	checks["private_proc"] = namespaceErr == nil && strings.HasPrefix(namespace, "pid:[") && separate && initErr == nil && strings.Contains(string(initLine), "wall-init")
	checks["no_direct_tcp"] = directTCPDenied()
	checks["no_direct_udp"] = directUDPDenied()
	return checks
}

func directTCPDenied() bool {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return denied(err)
	}
	defer unix.Close(fd)
	err = unix.Connect(fd, &unix.SockaddrInet4{Port: 9, Addr: [4]byte{198, 51, 100, 1}})
	if denied(err) {
		return true
	}
	if !errors.Is(err, unix.EINPROGRESS) {
		return false
	}
	ready := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
	if n, err := unix.Poll(ready, 2000); err != nil || n == 0 {
		return false
	}
	code, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	return err == nil && denied(unix.Errno(code))
}

func directUDPDenied() bool {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return denied(err)
	}
	defer unix.Close(fd)
	return denied(unix.Sendto(fd, []byte("e2e-denied"), 0, &unix.SockaddrInet4{Port: 9, Addr: [4]byte{198, 51, 100, 1}}))
}

func routeDenied() bool {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return errors.Is(err, unix.EPERM)
	}
	defer unix.Close(fd)
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2})
	request := make([]byte, unix.NLMSG_HDRLEN+unix.SizeofIfAddrmsg)
	binary.NativeEndian.PutUint32(request, uint32(len(request)))
	binary.NativeEndian.PutUint16(request[4:], unix.RTM_NEWADDR)
	binary.NativeEndian.PutUint16(request[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE)
	binary.NativeEndian.PutUint32(request[8:], 1)
	request[unix.NLMSG_HDRLEN] = unix.AF_INET
	binary.NativeEndian.PutUint32(request[unix.NLMSG_HDRLEN+4:], 1)
	if err := unix.Sendto(fd, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return errors.Is(err, unix.EPERM)
	}
	var reply [4096]byte
	n, _, err := unix.Recvfrom(fd, reply[:], 0)
	return err == nil && n >= 20 && binary.NativeEndian.Uint16(reply[4:]) == unix.NLMSG_ERROR && int32(binary.NativeEndian.Uint32(reply[16:])) == -int32(unix.EPERM)
}
