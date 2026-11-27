//go:build linux

package guest

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/sys/unix"
)

func vmLinkInfo(index int, flags uint32) []byte {
	data := make([]byte, 16)
	binary.NativeEndian.PutUint32(data[4:], uint32(index))
	binary.NativeEndian.PutUint32(data[8:], flags)
	binary.NativeEndian.PutUint32(data[12:], 0xffffffff)
	return data
}

func setupVMRoutes() error {
	loop, err := net.InterfaceByName("lo")
	if err != nil {
		return err
	}
	if _, err := vmNetlink(unix.NETLINK_ROUTE, []vmNLMessage{{unix.RTM_NEWLINK, unix.NLM_F_REQUEST | unix.NLM_F_ACK, vmLinkInfo(loop.Index, unix.IFF_UP)}}); err != nil {
		return err
	}
	create := vmNLJoin(vmLinkInfo(0, 0), vmNLAttr(unix.IFLA_IFNAME, vmNLString("dummy0")), vmNLAttr(unix.IFLA_LINKINFO|unix.NLA_F_NESTED, vmNLAttr(unix.IFLA_INFO_KIND, vmNLString("dummy"))))
	_, err = vmNetlink(unix.NETLINK_ROUTE, []vmNLMessage{{unix.RTM_NEWLINK, unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_EXCL, create}})
	if err != nil && err != unix.EEXIST {
		return fmt.Errorf("make guest dummy0: %w", err)
	}
	dummy, err := net.InterfaceByName("dummy0")
	if err != nil {
		return err
	}
	replies, err := vmNetlink(unix.NETLINK_ROUTE, []vmNLMessage{{unix.RTM_GETLINK, unix.NLM_F_REQUEST | unix.NLM_F_ACK, vmLinkInfo(dummy.Index, 0)}})
	if err != nil {
		return err
	}
	valid := false
	for _, reply := range replies {
		if reply.kind != unix.RTM_NEWLINK || len(reply.data) < 16 {
			continue
		}
		attrs, err := vmNLAttrs(reply.data[16:])
		if err != nil {
			return err
		}
		nested, err := vmNLAttrs(attrs[unix.IFLA_LINKINFO])
		if err != nil {
			return err
		}
		if strings.TrimRight(string(nested[unix.IFLA_INFO_KIND]), "\x00") == "dummy" {
			valid = true
		}
	}
	if !valid {
		return vmError("the guest dummy0 name belongs to an unexpected interface kind")
	}
	if _, err := vmNetlink(unix.NETLINK_ROUTE, []vmNLMessage{{unix.RTM_NEWLINK, unix.NLM_F_REQUEST | unix.NLM_F_ACK, vmLinkInfo(dummy.Index, unix.IFF_UP)}}); err != nil {
		return err
	}
	for _, address := range []string{"192.0.2.1", "fd00::1"} {
		ip := netip.MustParseAddr(address)
		family := byte(unix.AF_INET6)
		bits := byte(128)
		bytes := ip.AsSlice()
		if ip.Is4() {
			family = unix.AF_INET
			bits = 32
		}
		flags := byte(0)
		if ip.Is6() {
			// A dummy interface has no neighbour to answer DAD. The fixed
			// guest-only address must be usable before the first seed stream.
			flags = unix.IFA_F_NODAD
		}
		body := []byte{family, bits, flags, unix.RT_SCOPE_UNIVERSE, 0, 0, 0, 0}
		binary.NativeEndian.PutUint32(body[4:], uint32(dummy.Index))
		body = vmNLJoin(body, vmNLAttr(unix.IFA_LOCAL, bytes), vmNLAttr(unix.IFA_ADDRESS, bytes))
		if _, err := vmNetlink(unix.NETLINK_ROUTE, []vmNLMessage{{unix.RTM_NEWADDR, unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_EXCL, body}}); err != nil && err != unix.EEXIST {
			return err
		}
		// There is no NIC/veth: only this dummy default route. UDP/ICMP can
		// reach no outside endpoint; nft redirects external TCP to the shim.
		route := []byte{family, 0, 0, 0, unix.RT_TABLE_MAIN, unix.RTPROT_STATIC, unix.RT_SCOPE_LINK, unix.RTN_UNICAST, 0, 0, 0, 0}
		route = vmNLJoin(route, vmNLAttr(unix.RTA_OIF, vmNLU32(uint32(dummy.Index))))
		if _, err := vmNetlink(unix.NETLINK_ROUTE, []vmNLMessage{{unix.RTM_NEWROUTE, unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_EXCL, route}}); err != nil && err != unix.EEXIST {
			return err
		}
	}
	return nil
}
