//go:build linux

package guest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"unsafe"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	soOriginalDst = 80 // Linux <linux/netfilter_ipv4.h> SO_ORIGINAL_DST

	iflaIfName   = 3
	iflaLinkInfo = 18
	iflaInfoKind = 1
	iflaInfoData = 2
	vethInfoPeer = 1

	ifaLocal   = 2
	ifaAddress = 1

	rtaDst = 1
	rtaOif = 4
)

// setupMicroVMNetwork configures the guest's loopback, dummy0 default route,
// and nftables transparent TCP redirect + UDP/ICMP/IPv6 reject rules inside a
// microvm (isolation spec 7.2, 20.3).
func setupMicroVMNetwork(redirectPort uint16, docker bool) error {
	if err := bringUpLoopback(); err != nil {
		return err
	}
	if err := setupDummyInterface(); err != nil {
		return err
	}
	if err := installTransparentNFT(redirectPort, docker); err != nil {
		return err
	}
	return nil
}

// setupDummyInterface creates dummy0 (falling back to a veth pair dummy0/dummy0p
// when the guest kernel lacks CONFIG_DUMMY, as in Kata 6.18.62), brings it up,
// assigns DummyIfaceIP/30, and installs the IPv4 default route via dummy0.
func setupDummyInterface() error {
	if err := createLinkNetlink(buildNewLinkDummy(DummyIfaceName)); err != nil {
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("create %s: %w. Try: check guest kernel netlink support", DummyIfaceName, err)
		}
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) {
			if verr := createLinkNetlink(buildNewLinkVeth(DummyIfaceName, DummyPeerName)); verr != nil && !errors.Is(verr, unix.EEXIST) {
				return fmt.Errorf("create fallback veth %s: %w. Try: check CONFIG_VETH in the guest kernel", DummyIfaceName, verr)
			}
			_ = bringUpInterface(DummyPeerName)
		}
	}
	if err := bringUpInterface(DummyIfaceName); err != nil {
		return err
	}
	iface, err := net.InterfaceByName(DummyIfaceName)
	if err != nil {
		return fmt.Errorf("lookup %s: %w", DummyIfaceName, err)
	}
	ip := netip.MustParseAddr(DummyIfaceIP).As4()
	if err := sendNetlinkRoute(buildNewAddr(iface.Index, ip, DummyIfacePrefixLen)); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("assign %s/%d to %s: %w", DummyIfaceIP, DummyIfacePrefixLen, DummyIfaceName, err)
	}
	if err := sendNetlinkRoute(buildDefaultRoute(iface.Index)); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("add default route via %s: %w", DummyIfaceName, err)
	}
	return nil
}

func bringUpInterface(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open socket for %s: %w", name, err)
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("prepare ifreq for %s: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("get flags for %s: %w", name, err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("set %s up: %w", name, err)
	}
	return nil
}

// installTransparentNFT programs the guest kernel's nftables subsystem over
// NETLINK_NETFILTER (no external nft binary required):
//   - ip wall_nat output: skips lo and 127.0.0.0/8 (and docker0 when docker is
//     on) and redirects all other IPv4 TCP to redirectPort (3129).
//   - inet wall_filter output: allows loopback and redirected IPv4 TCP, and
//     rejects all other non-loopback traffic (UDP, ICMP, IPv6) immediately.
func installTransparentNFT(redirectPort uint16, docker bool) error {
	if redirectPort == 0 {
		redirectPort = DefaultTransparentPort
	}
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		return fmt.Errorf("nftables: lookup lo interface: %w", err)
	}
	loIdx := uint32(lo.Index) //nolint:gosec // G115: interface index is small positive int

	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("nftables: open netlink connection: %w. Try: check CONFIG_NF_TABLES in the guest kernel", err)
	}

	natTable := c.AddTable(&nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   "wall_nat",
	})
	natOut := c.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    natTable,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityNATDest,
	})
	// Skip redirect for loopback interface.
	c.AddRule(&nftables.Rule{
		Table: natTable,
		Chain: natOut,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyOIF, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(loIdx)},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})
	// Skip redirect for 127.0.0.0/8 (explicit proxy :3128, transparent shim :3129, DNS stub :53).
	c.AddRule(&nftables.Rule{
		Table: natTable,
		Chain: natOut,
		Exprs: []expr.Any{
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{127}},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})
	if docker {
		c.AddRule(&nftables.Rule{
			Table: natTable,
			Chain: natOut,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: nftIfName("docker0")},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
	}
	// Redirect all remaining IPv4 TCP to the transparent shim port.
	c.AddRule(&nftables.Rule{
		Table: natTable,
		Chain: natOut,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
			&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(redirectPort)},
			&expr.Redir{RegisterProtoMin: 1},
		},
	})

	if docker {
		natPre := c.AddChain(&nftables.Chain{
			Name:     "prerouting",
			Table:    natTable,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookPrerouting,
			Priority: nftables.ChainPriorityNATDest,
		})
		// Container traffic to loopback or Docker's private bridge subnet stays local; all other TCP redirects.
		c.AddRule(&nftables.Rule{
			Table: natTable,
			Chain: natPre,
			Exprs: []expr.Any{
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{127}},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
		c.AddRule(&nftables.Rule{
			Table: natTable,
			Chain: natPre,
			Exprs: []expr.Any{
				&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 2},
				&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 2, Mask: []byte{0xff, 0xf0}, Xor: []byte{0, 0}},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{172, 16}},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
		c.AddRule(&nftables.Rule{
			Table: natTable,
			Chain: natPre,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
				&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(redirectPort)},
				&expr.Redir{RegisterProtoMin: 1},
			},
		})
	}

	filterTable := c.AddTable(&nftables.Table{
		Family: nftables.TableFamilyINet,
		Name:   "wall_filter",
	})
	acceptPolicy := nftables.ChainPolicyAccept
	filterOut := c.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    filterTable,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &acceptPolicy,
	})
	// Allow loopback interface.
	c.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: filterOut,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyOIF, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(loIdx)},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})
	if docker {
		c.AddRule(&nftables.Rule{
			Table: filterTable,
			Chain: filterOut,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: nftIfName("docker0")},
				&expr.Verdict{Kind: expr.VerdictAccept},
			},
		})
	}
	// Allow IPv4 127.0.0.0/8.
	c.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: filterOut,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{127}},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})
	// Allow IPv6 ::1.
	var ipv6Loopback [16]byte
	ipv6Loopback[15] = 1
	c.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: filterOut,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV6}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ipv6Loopback[:]},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})
	// Allow IPv4 TCP (which wall_nat redirects to the transparent shim).
	c.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: filterOut,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})
	// Reject all other outbound traffic (UDP, ICMP, non-loopback IPv6) immediately.
	c.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: filterOut,
		Exprs: []expr.Any{
			&expr.Reject{
				Type: unix.NFT_REJECT_ICMPX_UNREACH,
				Code: unix.NFT_REJECT_ICMPX_ADMIN_PROHIBITED,
			},
		},
	})

	if err := c.Flush(); err != nil {
		return fmt.Errorf("nftables: flush rules: %w. Try: check CONFIG_NFT_REDIR and CONFIG_NFT_REJECT in the guest kernel", err)
	}
	return nil
}

func nftIfName(name string) []byte {
	var b [16]byte
	copy(b[:], name)
	return b[:]
}

// originalDst recovers the pre-NAT IPv4 destination address and port of an
// nftables-redirected TCP connection via SO_ORIGINAL_DST (spec 7.2).
func originalDst(conn net.Conn) (netip.AddrPort, error) {
	sc, ok := conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return netip.AddrPort{}, errors.New("connection does not expose SyscallConn")
	}
	rawConn, err := sc.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("syscall conn: %w", err)
	}
	var (
		raw [16]byte // struct sockaddr_in
		op  error
	)
	err = rawConn.Control(func(fd uintptr) {
		sz := uint32(len(raw))
		//nolint:gosec // G103: Linux SO_ORIGINAL_DST requires passing a sockaddr_in pointer to getsockopt
		_, _, errno := unix.Syscall6(
			unix.SYS_GETSOCKOPT,
			fd,
			uintptr(unix.SOL_IP),
			uintptr(soOriginalDst),
			uintptr(unsafe.Pointer(&raw[0])),
			uintptr(unsafe.Pointer(&sz)),
			0,
		)
		if errno != 0 {
			op = errno
		}
	})
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("control fd for SO_ORIGINAL_DST: %w", err)
	}
	if op != nil {
		return netip.AddrPort{}, fmt.Errorf("getsockopt SO_ORIGINAL_DST: %w", op)
	}
	return parseSockaddrIn(raw[:])
}

func parseSockaddrIn(raw []byte) (netip.AddrPort, error) {
	if len(raw) < 8 {
		return netip.AddrPort{}, errors.New("short sockaddr_in")
	}
	family := binary.NativeEndian.Uint16(raw[0:2])
	if family != unix.AF_INET {
		return netip.AddrPort{}, fmt.Errorf("unexpected SO_ORIGINAL_DST family %d", family)
	}
	port := binary.BigEndian.Uint16(raw[2:4])
	var ip4 [4]byte
	copy(ip4[:], raw[4:8])
	return netip.AddrPortFrom(netip.AddrFrom4(ip4), port), nil
}

// --- Minimal RTNETLINK builders for dummy0 / veth / address / default route ---

func createLinkNetlink(msg []byte) error { return sendNetlinkRoute(msg) }

func sendNetlinkRoute(msg []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("open NETLINK_ROUTE socket: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("bind NETLINK_ROUTE socket: %w", err)
	}
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return fmt.Errorf("send NETLINK_ROUTE: %w", err)
	}
	resp := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, resp, 0)
	if err != nil {
		return fmt.Errorf("recv NETLINK_ROUTE ack: %w", err)
	}
	if n < 20 {
		return errors.New("short NETLINK_ROUTE ack")
	}
	mtype := binary.NativeEndian.Uint16(resp[4:6])
	if mtype == unix.NLMSG_ERROR {
		//nolint:gosec // G115: Linux nlmsgerr.error is a signed int32 negated errno
		code := int32(binary.NativeEndian.Uint32(resp[16:20]))
		if code == 0 {
			return nil
		}
		if code < 0 {
			return unix.Errno(uint32(-code))
		}
		return unix.Errno(uint32(code))
	}
	return nil
}

func appendRtAttr(dst []byte, attrType uint16, payload []byte) []byte {
	rtaLen := 4 + len(payload)
	padded := (rtaLen + 3) &^ 3
	out := make([]byte, padded)
	binary.NativeEndian.PutUint16(out[0:2], uint16(rtaLen)) //nolint:gosec // G115: small RTNETLINK attribute
	binary.NativeEndian.PutUint16(out[2:4], attrType)
	copy(out[4:], payload)
	return append(dst, out...)
}

func wrapNetlinkMsg(msgType, flags uint16, body []byte) []byte {
	total := 16 + len(body)
	hdr := make([]byte, 16, total)
	binary.NativeEndian.PutUint32(hdr[0:4], uint32(total)) //nolint:gosec // G115: small RTNETLINK message
	binary.NativeEndian.PutUint16(hdr[4:6], msgType)
	binary.NativeEndian.PutUint16(hdr[6:8], flags)
	binary.NativeEndian.PutUint32(hdr[8:12], 1)
	return append(hdr, body...)
}

func buildNewLinkDummy(name string) []byte {
	// struct ifinfomsg (16 bytes zeroed) + IFLA_IFNAME + IFLA_LINKINFO(IFLA_INFO_KIND="dummy")
	body := make([]byte, 16)
	body = appendRtAttr(body, iflaIfName, append([]byte(name), 0))
	linkInfo := appendRtAttr(nil, iflaInfoKind, append([]byte("dummy"), 0))
	body = appendRtAttr(body, iflaLinkInfo|unix.NLA_F_NESTED, linkInfo)
	flags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_EXCL)
	return wrapNetlinkMsg(unix.RTM_NEWLINK, flags, body)
}

func buildNewLinkVeth(name, peerName string) []byte {
	body := make([]byte, 16)
	body = appendRtAttr(body, iflaIfName, append([]byte(name), 0))
	peerMsg := make([]byte, 16) // struct ifinfomsg for peer
	peerMsg = appendRtAttr(peerMsg, iflaIfName, append([]byte(peerName), 0))
	infoData := appendRtAttr(nil, vethInfoPeer|unix.NLA_F_NESTED, peerMsg)
	linkInfo := appendRtAttr(nil, iflaInfoKind, append([]byte("veth"), 0))
	linkInfo = appendRtAttr(linkInfo, iflaInfoData|unix.NLA_F_NESTED, infoData)
	body = appendRtAttr(body, iflaLinkInfo|unix.NLA_F_NESTED, linkInfo)
	flags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_EXCL)
	return wrapNetlinkMsg(unix.RTM_NEWLINK, flags, body)
}

func buildNewAddr(ifIndex int, ip [4]byte, prefixLen uint8) []byte {
	// struct ifaddrmsg (8 bytes): family, prefixlen, flags, scope, index(uint32)
	body := make([]byte, 8)
	body[0] = unix.AF_INET
	body[1] = prefixLen
	body[3] = unix.RT_SCOPE_UNIVERSE
	binary.NativeEndian.PutUint32(body[4:8], uint32(ifIndex)) //nolint:gosec // G115: interface index is small positive int
	body = appendRtAttr(body, ifaLocal, ip[:])
	body = appendRtAttr(body, ifaAddress, ip[:])
	flags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_REPLACE)
	return wrapNetlinkMsg(unix.RTM_NEWADDR, flags, body)
}

func buildDefaultRoute(ifIndex int) []byte {
	// struct rtmsg (12 bytes): family, dst_len(0), src_len(0), tos, table, protocol, scope, type, flags(4)
	body := make([]byte, 12)
	body[0] = unix.AF_INET
	body[4] = unix.RT_TABLE_MAIN
	body[5] = unix.RTPROT_BOOT
	body[6] = unix.RT_SCOPE_LINK
	body[7] = unix.RTN_UNICAST
	var idxBuf [4]byte
	binary.NativeEndian.PutUint32(idxBuf[:], uint32(ifIndex)) //nolint:gosec // G115: interface index is small positive int
	body = appendRtAttr(body, rtaDst, []byte{0, 0, 0, 0})
	body = appendRtAttr(body, rtaOif, idxBuf[:])
	flags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_REPLACE)
	return wrapNetlinkMsg(unix.RTM_NEWROUTE, flags, body)
}
