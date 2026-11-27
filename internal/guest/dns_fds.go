package guest

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"syscall"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// DNSFDSEnv names the launcher variable that marks inherited DNS sockets on
// file descriptors 4 (UDP) and 5 (TCP). Without this variable, the guest init
// never probes descriptors 4 or 5.
var DNSFDSEnv = brand.Env("DNS_FDS")

const (
	inheritedDNSFDSValue = "4,5"
	inheritedDNSUDPFD    = 4
	inheritedDNSTCPFD    = 5
	inheritedDNSPort     = 53
)

// DNSSockets holds pre-bound loopback UDP and TCP sockets for the guest DNS
// stub.
type DNSSockets struct {
	UDP net.PacketConn
	TCP net.Listener
}

// Close closes any non-nil UDP or TCP socket in s.
func (s DNSSockets) Close() error {
	var firstErr error
	if s.UDP != nil {
		if err := s.UDP.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.TCP != nil {
		if err := s.TCP.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func claimInheritedDNSSockets() (DNSSockets, error) {
	raw, present := os.LookupEnv(DNSFDSEnv)
	_ = os.Unsetenv(DNSFDSEnv)
	if !present || raw == "" {
		return DNSSockets{}, nil
	}
	if raw != inheritedDNSFDSValue {
		return DNSSockets{}, &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("invalid %s value %q", DNSFDSEnv, raw),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	socks, err := claimDNSSocketFDs(inheritedDNSUDPFD, inheritedDNSTCPFD, inheritedDNSPort)
	if err != nil {
		return DNSSockets{}, err
	}
	for _, addr := range []net.Addr{socks.UDP.LocalAddr(), socks.TCP.Addr()} {
		if err := verifyInheritedDNSAddr(addr); err != nil {
			_ = socks.Close()
			return DNSSockets{}, err
		}
	}
	return socks, nil
}

func verifyInheritedDNSAddr(addr net.Addr) error {
	if err := verifyLoopbackDNSAddr(addr, inheritedDNSPort); err != nil {
		return err
	}
	if addr.String() != "127.0.0.53:53" {
		return &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("inherited DNS socket address %s is not 127.0.0.53:53", addr),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	return nil
}

func claimDNSSocketFDs(udpFD, tcpFD, wantPort int) (DNSSockets, error) {
	udpType, errU := syscall.GetsockoptInt(udpFD, syscall.SOL_SOCKET, syscall.SO_TYPE)
	tcpType, errT := syscall.GetsockoptInt(tcpFD, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if errU == nil {
		syscall.CloseOnExec(udpFD)
	}
	if errT == nil {
		syscall.CloseOnExec(tcpFD)
	}
	closeRaw := func() {
		if errU == nil {
			_ = syscall.Close(udpFD)
		}
		if errT == nil {
			_ = syscall.Close(tcpFD)
		}
	}
	if errU != nil || udpType != syscall.SOCK_DGRAM {
		closeRaw()
		return DNSSockets{}, &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("inherited DNS fd %d is not a UDP datagram socket", udpFD),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	if errT != nil || tcpType != syscall.SOCK_STREAM {
		closeRaw()
		return DNSSockets{}, &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("inherited DNS fd %d is not a TCP stream socket", tcpFD),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	if !isListeningTCPSocket(tcpFD) {
		closeRaw()
		return DNSSockets{}, &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("inherited DNS fd %d is not a listening TCP socket", tcpFD),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}

	uf := os.NewFile(uintptr(udpFD), "guest-dns-udp") //nolint:gosec // G115: validated non-negative descriptor
	tf := os.NewFile(uintptr(tcpFD), "guest-dns-tcp") //nolint:gosec // G115: validated non-negative descriptor
	pc, errP := net.FilePacketConn(uf)
	ln, errL := net.FileListener(tf)
	_ = uf.Close()
	_ = tf.Close()
	if errP != nil {
		if ln != nil {
			_ = ln.Close()
		}
		return DNSSockets{}, &proto.Error{
			Code:    "wall_init",
			Message: "the guest init cannot adopt its UDP DNS socket: " + errP.Error(),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	if errL != nil {
		_ = pc.Close()
		return DNSSockets{}, &proto.Error{
			Code:    "wall_init",
			Message: "the guest init cannot adopt its TCP DNS socket: " + errL.Error(),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}

	socks := DNSSockets{UDP: pc, TCP: ln}
	if err := verifyLoopbackDNSAddr(pc.LocalAddr(), wantPort); err != nil {
		_ = socks.Close()
		return DNSSockets{}, err
	}
	if err := verifyLoopbackDNSAddr(ln.Addr(), wantPort); err != nil {
		_ = socks.Close()
		return DNSSockets{}, err
	}
	return socks, nil
}

func verifyLoopbackDNSAddr(addr net.Addr, wantPort int) error {
	if addr == nil {
		return &proto.Error{
			Code:    "wall_init",
			Message: "DNS socket has no bound local address",
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	var ip netip.Addr
	var port int
	switch v := addr.(type) {
	case *net.UDPAddr:
		if a, ok := netip.AddrFromSlice(v.IP); ok {
			ip = a
		}
		port = v.Port
	case *net.TCPAddr:
		if a, ok := netip.AddrFromSlice(v.IP); ok {
			ip = a
		}
		port = v.Port
	default:
		h, pStr, err := net.SplitHostPort(addr.String())
		if err == nil {
			ip, _ = netip.ParseAddr(h)
			port, _ = strconv.Atoi(pStr)
		}
	}
	if !ip.IsValid() || !ip.Unmap().IsLoopback() {
		return &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("DNS socket address %s is not on loopback", addr),
			Hint:    "Bind sandbox DNS sockets only on loopback, then resume the session.",
		}
	}
	if port < 1 || port > 65535 || (wantPort > 0 && port != wantPort) {
		return &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("DNS socket address %s is not bound to port %d", addr, wantPort),
			Hint:    "Update the host helper and its sandbox launcher, then resume the session.",
		}
	}
	return nil
}

func verifyScopedDNSAddr(address net.Addr, scope dnsBindScope) error {
	switch scope {
	case dnsLoopbackScope:
		return verifyLoopbackDNSAddr(address, 0)
	case dnsPrivateVMScope:
		return verifyVMDNSWildcard(address)
	default:
		return fmt.Errorf("unknown guest DNS bind scope")
	}
}
