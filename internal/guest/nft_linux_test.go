//go:build linux

package guest

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseSockaddrIn(t *testing.T) {
	var raw [16]byte
	binary.NativeEndian.PutUint16(raw[0:2], unix.AF_INET)
	binary.BigEndian.PutUint16(raw[2:4], 443)
	copy(raw[4:8], []byte{198, 18, 0, 7})

	got, err := parseSockaddrIn(raw[:])
	if err != nil {
		t.Fatalf("parseSockaddrIn: %v", err)
	}
	want := netip.MustParseAddrPort("198.18.0.7:443")
	if got != want {
		t.Errorf("parseSockaddrIn = %v, want %v", got, want)
	}

	binary.NativeEndian.PutUint16(raw[0:2], unix.AF_INET6)
	if _, err := parseSockaddrIn(raw[:]); err == nil {
		t.Error("expected AF_INET6 sockaddr to be rejected")
	}
	if _, err := parseSockaddrIn(raw[:4]); err == nil {
		t.Error("expected truncated sockaddr to be rejected")
	}
}

func TestNetlinkMessageBuilders(t *testing.T) {
	dummy := buildNewLinkDummy("dummy0")
	if got := int(binary.NativeEndian.Uint32(dummy[0:4])); got != len(dummy) {
		t.Errorf("dummy msg len header = %d, actual %d", got, len(dummy))
	}
	if got := binary.NativeEndian.Uint16(dummy[4:6]); got != unix.RTM_NEWLINK {
		t.Errorf("dummy msg type = %d, want RTM_NEWLINK (%d)", got, unix.RTM_NEWLINK)
	}

	veth := buildNewLinkVeth("dummy0", "dummy0p")
	if got := int(binary.NativeEndian.Uint32(veth[0:4])); got != len(veth) {
		t.Errorf("veth msg len header = %d, actual %d", got, len(veth))
	}
	if got := binary.NativeEndian.Uint16(veth[4:6]); got != unix.RTM_NEWLINK {
		t.Errorf("veth msg type = %d, want RTM_NEWLINK", got)
	}

	addr := buildNewAddr(2, [4]byte{192, 0, 2, 1}, 30)
	if got := binary.NativeEndian.Uint16(addr[4:6]); got != unix.RTM_NEWADDR {
		t.Errorf("addr msg type = %d, want RTM_NEWADDR", got)
	}
	if addr[16] != unix.AF_INET || addr[17] != 30 {
		t.Errorf("ifaddrmsg family=%d prefixlen=%d, want AF_INET/30", addr[16], addr[17])
	}

	route := buildDefaultRoute(2)
	if got := binary.NativeEndian.Uint16(route[4:6]); got != unix.RTM_NEWROUTE {
		t.Errorf("route msg type = %d, want RTM_NEWROUTE", got)
	}
	if route[16] != unix.AF_INET || route[17] != 0 || route[20] != unix.RT_TABLE_MAIN {
		t.Errorf("rtmsg header mismatch: family=%d dstlen=%d table=%d", route[16], route[17], route[20])
	}
}
