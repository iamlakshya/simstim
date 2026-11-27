//go:build linux

package guest

import (
	"encoding/binary"
	"net"
	"net/netip"

	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

// These closed expression/attribute indices are from Linuxv6.18 UAPI
// nf_tables.h. They are transport wire values, not an egress policy matcher.
const (
	vmNFDrop          = 0 // NF_DROP in Linuxv6.18 include/uapi/linux/netfilter.h
	vmNFTNewTable     = 0
	vmNFTNewChain     = 3
	vmNFTNewRule      = 6
	vmNFTMetaIIFName  = 6
	vmNFTMetaOIFName  = 7
	vmNFTMetaNFProto  = 15
	vmNFTMetaL4Proto  = 16
	vmTransparentPort = 3129
)

func vmNFHeader(family byte, resource uint16) []byte {
	data := []byte{family, 0, 0, 0}
	binary.BigEndian.PutUint16(data[2:], resource)
	return data
}
func vmNFTExpression(name string, attributes []byte) []byte {
	return vmNLAttr(1|unix.NLA_F_NESTED, vmNLJoin(vmNLAttr(1, vmNLString(name)), vmNLAttr(2|unix.NLA_F_NESTED, attributes)))
}
func vmNFTMeta(key uint32) []byte {
	return vmNFTExpression("meta", vmNLJoin(vmNLAttr(1, vmNFU32(1)), vmNLAttr(2, vmNFU32(key))))
}
func vmNFTCompare(value []byte) []byte { return vmNFTCompareOp(value, 0) }
func vmNFTCompareOp(value []byte, op uint32) []byte {
	return vmNFTExpression("cmp", vmNLJoin(vmNLAttr(1, vmNFU32(1)), vmNLAttr(2, vmNFU32(op)), vmNLAttr(3|unix.NLA_F_NESTED, vmNLAttr(1, value))))
}
func vmNFTMessage(kind uint16, data []byte) vmNLMessage {
	return vmNLMessage{uint16(unix.NFNL_SUBSYS_NFTABLES<<8) | kind, unix.NLM_F_REQUEST | unix.NLM_F_ACK | unix.NLM_F_CREATE | unix.NLM_F_EXCL, vmNLJoin(vmNFHeader(unix.NFPROTO_INET, 0), data)}
}
func vmNFTChain(table, name, kind string, hook uint32, priority int32) vmNLMessage {
	attributes := vmNLJoin(vmNLAttr(1, vmNFU32(hook)), vmNLAttr(2, vmNFU32(uint32(priority))))
	return vmNFTMessage(vmNFTNewChain, vmNLJoin(vmNLAttr(1, vmNLString(table)), vmNLAttr(3, vmNLString(name)), vmNLAttr(4|unix.NLA_F_NESTED, attributes), vmNLAttr(7, vmNLString(kind))))
}
func vmNFTRule(table, chain string, expressions []byte) vmNLMessage {
	return vmNFTMessage(vmNFTNewRule, vmNLJoin(vmNLAttr(1, vmNLString(table)), vmNLAttr(2, vmNLString(chain)), vmNLAttr(4|unix.NLA_F_NESTED, expressions)))
}
func vmNFTRedirect() []byte {
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, vmTransparentPort)
	immediate := vmNFTExpression("immediate", vmNLJoin(vmNLAttr(1, vmNFU32(1)), vmNLAttr(2|unix.NLA_F_NESTED, vmNLAttr(1, port))))
	return vmNLJoin(immediate, vmNFTExpression("redir", vmNLAttr(1, vmNFU32(1))))
}
func vmNFTDrop() []byte {
	verdict := vmNLAttr(2|unix.NLA_F_NESTED, vmNLAttr(1, vmNFU32(vmNFDrop)))
	return vmNFTExpression("immediate", vmNLJoin(vmNLAttr(1, vmNFU32(0)), vmNLAttr(2|unix.NLA_F_NESTED, verdict)))
}

func vmNFTDockerExternal() []byte {
	prefix := netip.MustParsePrefix(vmDockerPool)
	mask := net.CIDRMask(prefix.Bits(), 32)
	payload := vmNFTExpression("payload", vmNLJoin(vmNLAttr(1, vmNFU32(1)), vmNLAttr(2, vmNFU32(1)), vmNLAttr(3, vmNFU32(16)), vmNLAttr(4, vmNFU32(4))))
	bitwise := vmNFTExpression("bitwise", vmNLJoin(vmNLAttr(1, vmNFU32(1)), vmNLAttr(2, vmNFU32(1)), vmNLAttr(3, vmNFU32(4)), vmNLAttr(4|unix.NLA_F_NESTED, vmNLAttr(1, mask)), vmNLAttr(5|unix.NLA_F_NESTED, vmNLAttr(1, []byte{0, 0, 0, 0}))))
	// Preserve the configured bridge pool's local services and DNS gateway.
	// This is guest topology data; trusted egress still decides permissions.
	return vmNLJoin(vmNFTMeta(vmNFTMetaIIFName), vmNFTCompareOp(vmNLString("lo"), 1), vmNFTMeta(vmNFTMetaNFProto), vmNFTCompare([]byte{unix.NFPROTO_IPV4}), vmNFTMeta(vmNFTMetaL4Proto), vmNFTCompare([]byte{unix.IPPROTO_TCP}), payload, bitwise, vmNFTCompareOp(prefix.Masked().Addr().AsSlice(), 1), vmNFTRedirect())
}

func vmNFTRedirectMessages(docker bool) []vmNLMessage {
	table := brand.Name + "_vm"
	external := vmNLJoin(vmNFTMeta(vmNFTMetaOIFName), vmNFTCompare(vmNLString("dummy0")))
	tcp := vmNLJoin(external, vmNFTMeta(vmNFTMetaL4Proto), vmNFTCompare([]byte{unix.IPPROTO_TCP}), vmNFTRedirect())
	nonTCP := vmNLJoin(external, vmNFTMeta(vmNFTMetaL4Proto), vmNFTCompareOp([]byte{unix.IPPROTO_TCP}, 1), vmNFTDrop())
	messages := []vmNLMessage{
		{unix.NFNL_MSG_BATCH_BEGIN, unix.NLM_F_REQUEST, vmNFHeader(0, unix.NFNL_SUBSYS_NFTABLES)},
		vmNFTMessage(vmNFTNewTable, vmNLAttr(1, vmNLString(table))),
		vmNFTChain(table, "output", "nat", 3, -100), vmNFTRule(table, "output", tcp),
		vmNFTChain(table, "non_tcp", "filter", 3, 0), vmNFTRule(table, "non_tcp", nonTCP),
	}
	if docker {
		messages = append(messages, vmNFTChain(table, "containers", "nat", 0, -100), vmNFTRule(table, "containers", vmNFTDockerExternal()))
	}
	return append(messages, vmNLMessage{unix.NFNL_MSG_BATCH_END, unix.NLM_F_REQUEST, vmNFHeader(0, unix.NFNL_SUBSYS_NFTABLES)})
}
func installVMRedirect(docker bool) error {
	_, err := vmNetlink(unix.NETLINK_NETFILTER, vmNFTRedirectMessages(docker))
	return err
}
