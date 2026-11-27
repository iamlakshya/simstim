package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// testPlainDNS, when non-empty in a test, sets the sandbox DNS mode ("real" or
// "synthetic") under the plain and fake test drivers.
var testPlainDNS string

const (
	dnsMinPacketBytes = 12
	dnsForwardTimeout = 5 * time.Second
)

func dnsModeForManifest(m *host.Manifest) string {
	if m == nil || m.Wall == nil {
		return ""
	}
	return dnsModeForDriver(m.Wall.Driver)
}

func dnsModeForDriver(driver string) string {
	switch driver {
	case driverPlain, "fake":
		return testPlainDNS
	case "process", "gvisor":
		return proto.DNSReal
	case "chv", "firecracker", "nitro":
		return proto.DNSSynthetic
	default:
		return ""
	}
}

func (s *Supervisor) forwardDNSStream(guestSt *link.Stream) {
	defer guestSt.Close()
	s.mu.Lock()
	ep := s.eg
	s.mu.Unlock()
	if ep == nil || ep.dead.Load() {
		return
	}
	var raw json.RawMessage
	if err := guestSt.Header(&raw); err != nil {
		return
	}
	if err := egress.ParseDNSStreamHeader(raw); err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dnsForwardTimeout)
	defer cancel()
	egSt, err := ep.sess.Open(ctx, link.KindDNS, nil)
	if err != nil {
		return
	}
	defer egSt.Close()

	stop := context.AfterFunc(ctx, func() {
		_ = guestSt.Close()
		_ = egSt.Close()
	})
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, copyErr := io.Copy(egSt, io.LimitReader(guestSt, egress.MaxDNSMessageBytes+1))
		if copyErr != nil || n < dnsMinPacketBytes || n > egress.MaxDNSMessageBytes {
			_ = egSt.Close()
			_ = guestSt.Close()
			return
		}
		_ = egSt.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		n, copyErr := io.Copy(guestSt, io.LimitReader(egSt, egress.MaxDNSMessageBytes+1))
		if copyErr != nil || n == 0 || n > egress.MaxDNSMessageBytes {
			_ = guestSt.Close()
			_ = egSt.Close()
			return
		}
		_ = guestSt.CloseWrite()
	}()
	wg.Wait()
}
