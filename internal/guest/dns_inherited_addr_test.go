package guest

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestInheritedDNSRequiresExactStubEndpoint(t *testing.T) {
	for _, ip := range []net.IP{net.IPv4(127, 0, 0, 53), net.IPv4(127, 0, 0, 1), net.IPv4zero, net.IPv6loopback} {
		for _, port := range []int{53, 5353} {
			for _, addr := range []net.Addr{&net.UDPAddr{IP: ip, Port: port}, &net.TCPAddr{IP: ip, Port: port}} {
				err := verifyInheritedDNSAddr(addr)
				want := ip.Equal(net.IPv4(127, 0, 0, 53)) && port == 53
				if want {
					if err != nil {
						t.Errorf("valid inherited address %s refused: %v", addr, err)
					}
					continue
				}
				var pe *proto.Error
				if !errors.As(err, &pe) || pe.Code != "wall_init" || pe.Hint == "" {
					t.Errorf("inherited address %s = %v, want typed error with hint", addr, err)
				}
			}
		}
	}
}

func TestInheritedDNSRequiresListeningTCP(t *testing.T) {
	udpFD, oldTCP, _ := makeRawLoopbackSocketFDs(t)
	_ = syscall.Close(oldTCP)
	tcpFD, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		_ = syscall.Close(udpFD)
		t.Fatal(err)
	}
	_, err = claimDNSSocketFDs(udpFD, tcpFD, 0)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "wall_init" || pe.Hint == "" {
		t.Fatalf("non-listening TCP fd = %v, want typed error with hint", err)
	}
	for _, fd := range []int{udpFD, tcpFD} {
		if _, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE); !errors.Is(err, syscall.EBADF) {
			t.Errorf("rejected raw fd %d remains open: %v", fd, err)
		}
	}
}
