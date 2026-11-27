//go:build linux

package host

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type custodyPartialReleaseConnV1 struct {
	writes    int
	sent      int
	failAfter int
}

func (c *custodyPartialReleaseConnV1) Read([]byte) (int, error) { return 0, io.EOF }
func (c *custodyPartialReleaseConnV1) Write(b []byte) (int, error) {
	c.writes++
	n := min(len(b), c.failAfter)
	c.sent += n
	return n, io.ErrUnexpectedEOF
}
func (*custodyPartialReleaseConnV1) Close() error                     { return nil }
func (*custodyPartialReleaseConnV1) LocalAddr() net.Addr              { return nil }
func (*custodyPartialReleaseConnV1) RemoteAddr() net.Addr             { return nil }
func (*custodyPartialReleaseConnV1) SetDeadline(time.Time) error      { return nil }
func (*custodyPartialReleaseConnV1) SetReadDeadline(time.Time) error  { return nil }
func (*custodyPartialReleaseConnV1) SetWriteDeadline(time.Time) error { return nil }

func TestCustodyReleasePartialSendPermanentlyCrossesFactoryAdmission(t *testing.T) {
	for _, prefix := range []int{0, 1, 4, 9} {
		conn := &custodyPartialReleaseConnV1{failAfter: prefix}
		launch := &startupLaunchV1{line: &StartupLineV1{conn: conn}}
		if launch.custodyReleaseSent {
			t.Fatal("factory admission occurred before original send")
		}
		if err := launch.sendCustodyReleaseV1(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatal("partial release fault was lost", err)
		}
		if !launch.custodyReleaseSent {
			t.Fatal("partial or uncertain send reset possible factory admission")
		}
		writes := conn.writes
		if err := launch.sendCustodyReleaseV1(); err == nil || conn.writes != writes {
			t.Fatal("uncertain release was retransmitted as a fresh factory grant")
		}
	}
}

func TestCopiedCustodyFlagsCannotSupplyReleasedMainOrCancel(t *testing.T) {
	for _, owner := range []*OwnedSupervisorLease{nil, {}, {custody: &custodyInheritanceV1{}}} {
		if owner.ManagerCustodyReleasedV1() {
			t.Fatal("copied flags issued an actual inherited released main")
		}
		if err := owner.CheckManagerCustodyCancelV1(&StartupLineV1{}); err == nil {
			t.Fatal("a data-only cancellation reached native cleanup authority")
		}
	}
}
