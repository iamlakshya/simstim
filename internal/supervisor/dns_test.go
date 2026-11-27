package supervisor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type dnsGuestSandbox struct {
	t       *testing.T
	rootDir string
	udpAddr string
	tcpAddr string
	supConn net.Conn
	gConn   net.Conn
	g       *guest.Init
	once    sync.Once
}

func newDNSGuestSandbox(t *testing.T) *dnsGuestSandbox {
	t.Helper()
	rootDir, err := os.MkdirTemp("/tmp", "supdns")
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(rootDir); err == nil {
		rootDir = real
	}
	t.Cleanup(func() { _ = os.RemoveAll(rootDir) })
	public, err := newTestPublicImageRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := installTestGuestImageRoots(rootDir, public); err != nil {
		t.Fatal(err)
	}

	var lc net.ListenConfig
	udpConn, err := lc.ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpLn, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		_ = udpConn.Close()
		t.Fatal(err)
	}

	ours, theirs, err := guest.SocketPair()
	if err != nil {
		_ = udpConn.Close()
		_ = tcpLn.Close()
		t.Fatal(err)
	}
	gConn, err := net.FileConn(theirs)
	_ = theirs.Close()
	if err != nil {
		_ = ours.Close()
		_ = udpConn.Close()
		_ = tcpLn.Close()
		t.Fatal(err)
	}

	g := guest.New(gConn, rootDir, slog.New(slog.DiscardHandler))
	g.SetDNSSockets(guest.DNSSockets{UDP: udpConn, TCP: tcpLn})
	return &dnsGuestSandbox{
		t:       t,
		rootDir: rootDir,
		udpAddr: udpConn.LocalAddr().String(),
		tcpAddr: tcpLn.Addr().String(),
		supConn: ours,
		gConn:   gConn,
		g:       g,
	}
}

func (d *dnsGuestSandbox) Start(_ context.Context) (net.Conn, error) {
	go d.g.Serve()
	return d.supConn, nil
}

func (d *dnsGuestSandbox) Kill() error {
	d.once.Do(func() {
		_ = d.gConn.Close()
		<-d.g.Done()
	})
	return nil
}

func (d *dnsGuestSandbox) Wait() error {
	<-d.g.Done()
	return nil
}

func buildSupDNSQuery(t *testing.T, id uint16, qname string, qtype dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(qname),
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	pkt, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return pkt
}

func querySupUDP(t *testing.T, addr string, raw []byte) dnsmessage.Message {
	t.Helper()
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(raw); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("UDP read: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(buf[:n]); err != nil {
		t.Fatalf("UDP unpack: %v", err)
	}
	return msg
}

func querySupTCP(t *testing.T, addr string, raw []byte) dnsmessage.Message {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	frame := make([]byte, 2+len(raw))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(raw)))
	copy(frame[2:], raw)
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		t.Fatalf("TCP length read: %v", err)
	}
	resp := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("TCP body read: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(resp); err != nil {
		t.Fatalf("TCP unpack: %v", err)
	}
	return msg
}

func readLookupCount(logPath string) int {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func startDNSSession(t *testing.T, id, dnsMode string, layers []api.Layer, agentArgv []string, lookupLog string) (*Supervisor, *dnsGuestSandbox) {
	t.Helper()
	dir := testFolders(t)
	pol, mergedLayers, err := egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}

	oldDNS, oldEnv := testPlainDNS, testEgressEnv
	testPlainDNS = dnsMode
	testEgressEnv = []string{"TEST_EGRESS_LOOKUP_LOG=" + lookupLog}
	t.Cleanup(func() {
		testPlainDNS = oldDNS
		testEgressEnv = oldEnv
	})

	sb := newDNSGuestSandbox(t)
	oldSandboxFor := sandboxFor
	sandboxFor = func(*host.Manifest) (sandbox, error) { return sb, nil }
	t.Cleanup(func() { sandboxFor = oldSandboxFor })

	t0 := time.Now().UTC()
	m := &host.Manifest{
		Schema:    1,
		ID:        id,
		Name:      "dns-box",
		CreatedAt: t0,
		Cwd:       dir,
		Agent: host.AgentInfo{
			Kind: "claude",
			Argv: agentArgv,
		},
		Env: []string{
			"PATH=/usr/bin:/bin",
			"HOME=" + host.GuestHome,
			brand.Env("HOOK_SOCK") + "=" + host.GuestHookSocket(),
			"TERM=xterm-256color",
		},
		Term:   host.TermInfo{Term: "xterm-256color", Cols: 80, Rows: 24},
		Status: host.Status{State: host.StateStarting, Since: t0},
		Wall:   &host.WallRecord{Level: "none", Driver: driverPlain, Version: version.Version, At: t0},
		Policy: &pol,
		Layers: mergedLayers,
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	supCh := make(chan *Supervisor, 1)
	testStarted = func(s *Supervisor) { supCh <- s }
	t.Cleanup(func() { testStarted = nil })

	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()

	var sup *Supervisor
	select {
	case sup = <-supCh:
	case err := <-ended:
		t.Fatalf("supervisor exited early: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for supervisor start")
	}
	t.Cleanup(func() {
		select {
		case sup.quit <- "test cleanup":
		default:
		}
		select {
		case <-ended:
		case <-time.After(15 * time.Second):
		}
	})
	return sup, sb
}

func TestDNSModeForDriverAndManifest(t *testing.T) {
	old := testPlainDNS
	testPlainDNS = ""
	defer func() { testPlainDNS = old }()

	if got := dnsModeForManifest(nil); got != "" {
		t.Fatalf("dnsModeForManifest(nil) = %q, want empty", got)
	}
	if got := dnsModeForManifest(&host.Manifest{}); got != "" {
		t.Fatalf("dnsModeForManifest(no wall) = %q, want empty", got)
	}
	for driver, want := range map[string]string{
		"":            "",
		driverPlain:   "",
		"fake":        "",
		"none":        "",
		"process":     proto.DNSReal,
		"gvisor":      proto.DNSReal,
		"chv":         proto.DNSSynthetic,
		"firecracker": proto.DNSSynthetic,
		"nitro":       proto.DNSSynthetic,
		"other":       "",
	} {
		if got := dnsModeForDriver(driver); got != want {
			t.Errorf("dnsModeForDriver(%q) = %q, want %q", driver, got, want)
		}
	}
}

func TestSupervisedDNSRealLivePolicyAndAttachByteExact(t *testing.T) {
	lookupLog := filepath.Join(t.TempDir(), "lookups.log")
	const marker = "\x1b[38;5;208mATTACH-EXACT-BYTES\x1b[0m"
	_, sb := startDNSSession(
		t,
		"dnsreal2",
		proto.DNSReal,
		[]api.Layer{{Kind: api.LayerSession, Preset: api.PresetBalanced}},
		[]string{"/bin/sh", "-c", "printf '%s\\n' '" + marker + "'; exec sleep 300"},
		lookupLog,
	)

	// 1. Denied host over UDP and TCP returns NXDOMAIN with zero upstream lookups.
	msgUDPDeny := querySupUDP(t, sb.udpAddr, buildSupDNSQuery(t, 1, "example.com.", dnsmessage.TypeA))
	msgTCPDeny := querySupTCP(t, sb.tcpAddr, buildSupDNSQuery(t, 2, "example.com.", dnsmessage.TypeAAAA))
	if msgUDPDeny.Header.RCode != dnsmessage.RCodeNameError || msgTCPDeny.Header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("denied example.com rcodes = UDP %v, TCP %v; want NXDOMAIN", msgUDPDeny.Header.RCode, msgTCPDeny.Header.RCode)
	}
	if got := readLookupCount(lookupLog); got != 0 {
		t.Fatalf("denied queries triggered %d upstream lookups, want 0", got)
	}

	// 2. Allowed host resolves over UDP and HTTPS/SVCB return NOERROR empty with 0 new lookups.
	msgAllow := querySupUDP(t, sb.udpAddr, buildSupDNSQuery(t, 3, "api.anthropic.com.", dnsmessage.TypeA))
	if msgAllow.Header.RCode != dnsmessage.RCodeSuccess || len(msgAllow.Answers) != 1 {
		t.Fatalf("allowed api.anthropic.com = rcode %v answers %d", msgAllow.Header.RCode, len(msgAllow.Answers))
	}
	if a, ok := msgAllow.Answers[0].Body.(*dnsmessage.AResource); !ok || netip.AddrFrom4(a.A) != netip.MustParseAddr("203.0.113.10") {
		t.Fatalf("allowed A body = %#v, want 203.0.113.10", msgAllow.Answers[0].Body)
	}
	afterAllow := readLookupCount(lookupLog)
	if afterAllow != 1 {
		t.Fatalf("lookup count after 1 allowed query = %d, want 1", afterAllow)
	}

	msgHTTPS := querySupTCP(t, sb.tcpAddr, buildSupDNSQuery(t, 4, "api.anthropic.com.", dnsmessage.TypeHTTPS))
	if msgHTTPS.Header.RCode != dnsmessage.RCodeSuccess || len(msgHTTPS.Answers) != 0 {
		t.Fatalf("HTTPS query = rcode %v answers %d, want NOERROR 0", msgHTTPS.Header.RCode, len(msgHTTPS.Answers))
	}
	if got := readLookupCount(lookupLog); got != afterAllow {
		t.Fatalf("HTTPS query triggered upstream lookup (%d -> %d)", afterAllow, got)
	}

	// 3. Live policy.add opens example.com, refuses metadata.internal (169.254.169.254), and policy.rm revokes example.com.
	rawRule, err := host.HolderRequest("dnsreal2", "policy.add", api.PolicyAddArgs{
		Allow:  true,
		Door:   api.DoorNet,
		Target: "example.com",
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("policy.add example.com: %v", err)
	}
	var added api.Rule
	if err := json.Unmarshal(rawRule, &added); err != nil {
		t.Fatal(err)
	}
	msgExOK := querySupTCP(t, sb.tcpAddr, buildSupDNSQuery(t, 5, "example.com.", dnsmessage.TypeA))
	if msgExOK.Header.RCode != dnsmessage.RCodeSuccess || len(msgExOK.Answers) != 1 {
		t.Fatalf("after policy.add, example.com = rcode %v answers %d", msgExOK.Header.RCode, len(msgExOK.Answers))
	}

	if _, err := host.HolderRequest("dnsreal2", "policy.add", api.PolicyAddArgs{
		Allow:  true,
		Door:   api.DoorNet,
		Target: "metadata.internal",
	}, 5*time.Second); err != nil {
		t.Fatalf("policy.add metadata.internal: %v", err)
	}
	msgMeta := querySupUDP(t, sb.udpAddr, buildSupDNSQuery(t, 6, "metadata.internal.", dnsmessage.TypeA))
	if msgMeta.Header.RCode != dnsmessage.RCodeRefused || len(msgMeta.Answers) != 0 {
		t.Fatalf("metadata.internal (169.254.169.254) = rcode %v answers %d, want REFUSED 0", msgMeta.Header.RCode, len(msgMeta.Answers))
	}

	if _, err := host.HolderRequest("dnsreal2", "policy.rm", api.PolicyRmArgs{ID: added.ID}, 5*time.Second); err != nil {
		t.Fatalf("policy.rm: %v", err)
	}
	beforeRevoked := readLookupCount(lookupLog)
	msgRevoked := querySupUDP(t, sb.udpAddr, buildSupDNSQuery(t, 7, "example.com.", dnsmessage.TypeA))
	if msgRevoked.Header.RCode != dnsmessage.RCodeNameError || readLookupCount(lookupLog) != beforeRevoked {
		t.Fatalf("after policy.rm, example.com = %v lookups=%d (was %d)", msgRevoked.Header.RCode, readLookupCount(lookupLog), beforeRevoked)
	}

	// 4. Attach over KindHolder while concurrent KindDNS traffic runs: terminal bytes remain byte-exact.
	var dnsWG sync.WaitGroup
	stopDNS := make(chan struct{})
	dnsWG.Add(2)
	go func() {
		defer dnsWG.Done()
		for {
			select {
			case <-stopDNS:
				return
			default:
				_ = querySupUDP(t, sb.udpAddr, buildSupDNSQuery(t, 8, "registry.npmjs.org.", dnsmessage.TypeA))
			}
		}
	}()
	go func() {
		defer dnsWG.Done()
		for {
			select {
			case <-stopDNS:
				return
			default:
				_ = querySupTCP(t, sb.tcpAddr, buildSupDNSQuery(t, 9, "example.com.", dnsmessage.TypeA))
			}
		}
	}()

	conn, r, w := dial(t, "dnsreal2")
	if err := w.WriteJSON(proto.TAttach, proto.Attach{Session: "dnsreal2", Cols: 80, Rows: 24, LastOffset: 0}); err != nil {
		close(stopDNS)
		dnsWG.Wait()
		t.Fatal(err)
	}
	var outBuf bytes.Buffer
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !bytes.Contains(outBuf.Bytes(), []byte(marker)) {
		f, err := proto.Read(r)
		if err != nil {
			break
		}
		if f.Type == proto.TOutput && len(f.Payload) >= 8 {
			outBuf.Write(f.Payload[8:])
		}
	}
	_ = conn.Close()
	close(stopDNS)
	dnsWG.Wait()

	if !bytes.Contains(outBuf.Bytes(), []byte(marker)) {
		t.Fatalf("attached terminal stream missing exact marker %q; got %q", marker, outBuf.String())
	}
}

func TestSupervisedDNSSyntheticZeroUpstreamAndPolicyUpdate(t *testing.T) {
	lookupLog := filepath.Join(t.TempDir(), "synth-lookups.log")
	_, sb := startDNSSession(
		t,
		"dnssyn23",
		proto.DNSSynthetic,
		[]api.Layer{{Kind: api.LayerSession, Preset: api.PresetBalanced, Unknown: api.UnknownAsk}},
		[]string{"/bin/sh", "-c", "exec sleep 300"},
		lookupLog,
	)

	msg1 := querySupUDP(t, sb.udpAddr, buildSupDNSQuery(t, 10, "unknown-one.example.", dnsmessage.TypeA))
	if msg1.Header.RCode != dnsmessage.RCodeSuccess || len(msg1.Answers) != 1 {
		t.Fatalf("synthetic unknown-one = rcode %v answers %d", msg1.Header.RCode, len(msg1.Answers))
	}
	addr1 := netip.AddrFrom4(msg1.Answers[0].Body.(*dnsmessage.AResource).A)
	if addr1 != netip.MustParseAddr("198.18.0.2") {
		t.Fatalf("first synthetic address = %s, want 198.18.0.2", addr1)
	}

	// Live explicit deny blocks unknown-one.example, while unknown-two.example gets 198.18.0.3 (no reuse).
	if _, err := host.HolderRequest("dnssyn23", "policy.add", api.PolicyAddArgs{
		Allow:  false,
		Door:   api.DoorNet,
		Target: "unknown-one.example",
	}, 5*time.Second); err != nil {
		t.Fatalf("policy.add deny: %v", err)
	}
	msgDenied := querySupTCP(t, sb.tcpAddr, buildSupDNSQuery(t, 11, "unknown-one.example.", dnsmessage.TypeA))
	if msgDenied.Header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("explicitly denied synthetic name = %v, want NXDOMAIN", msgDenied.Header.RCode)
	}
	msg2 := querySupUDP(t, sb.udpAddr, buildSupDNSQuery(t, 12, "unknown-two.example.", dnsmessage.TypeA))
	addr2 := netip.AddrFrom4(msg2.Answers[0].Body.(*dnsmessage.AResource).A)
	if addr2 != netip.MustParseAddr("198.18.0.3") {
		t.Fatalf("second synthetic address = %s, want 198.18.0.3", addr2)
	}
	if got := readLookupCount(lookupLog); got != 0 {
		t.Fatalf("synthetic session triggered %d upstream lookups, want 0", got)
	}
}

func TestSupervisorRejectsHostileDNSStreamAndFailsClosedWhenEgressDies(t *testing.T) {
	lookupLog := filepath.Join(t.TempDir(), "hostile-lookups.log")
	oldDNS, oldEnv := testPlainDNS, testEgressEnv
	testPlainDNS = proto.DNSReal
	testEgressEnv = []string{"TEST_EGRESS_LOOKUP_LOG=" + lookupLog}
	t.Cleanup(func() {
		testPlainDNS = oldDNS
		testEgressEnv = oldEnv
	})

	var sup *Supervisor
	supCh := make(chan *Supervisor, 1)
	testStarted = func(s *Supervisor) { supCh <- s }
	t.Cleanup(func() { testStarted = nil })

	id, fg, _ := supervised(t)
	gotAlerts := alertsTo(t)
	select {
	case sup = <-supCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out capturing *Supervisor")
	}

	rawAllowed := buildSupDNSQuery(t, 20, "api.anthropic.com.", dnsmessage.TypeA)
	rawDenied := buildSupDNSQuery(t, 21, "example.com.", dnsmessage.TypeA)

	// 1. Hostile guest headers attempting to pick DNS mode or inject fields are rejected before egress.
	for _, hdr := range []any{
		map[string]string{"mode": "synthetic"},
		map[string]string{"mode": "real"},
		map[string]string{"extra": "1"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		st, err := fg.sess.Open(ctx, link.KindDNS, hdr)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = st.Write(rawDenied)
		_ = st.CloseWrite()
		got, _ := io.ReadAll(st)
		_ = st.Close()
		if len(got) != 0 {
			t.Fatalf("forged KindDNS header %v returned %x, want empty close", hdr, got)
		}
	}
	// Undersized (< 12 bytes) and oversized (> 65535 bytes) payloads are closed without answer.
	for _, badBody := range [][]byte{
		{1, 2, 3, 4, 5},
		bytes.Repeat([]byte{0x41}, egress.MaxDNSMessageBytes+1),
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		st, err := fg.sess.Open(ctx, link.KindDNS, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = st.Write(badBody)
		_ = st.CloseWrite()
		got, _ := io.ReadAll(st)
		_ = st.Close()
		if len(got) != 0 {
			t.Fatalf("invalid DNS payload len %d returned %d bytes, want 0", len(badBody), len(got))
		}
	}
	if got := readLookupCount(lookupLog); got != 0 {
		t.Fatalf("hostile headers/frames caused %d lookups, want 0", got)
	}

	// 2. Valid empty header works while egress is alive.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	stOK, err := fg.sess.Open(ctx, link.KindDNS, nil)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = stOK.Write(rawAllowed)
	_ = stOK.CloseWrite()
	okBytes, err := io.ReadAll(stOK)
	_ = stOK.Close()
	if err != nil || len(okBytes) == 0 {
		t.Fatalf("valid KindDNS before kill = %d bytes, err=%v", len(okBytes), err)
	}

	// 3. Kill egress child via its *os.Process handle: KindDNS fails closed and no second egress starts.
	sup.mu.Lock()
	origPID := sup.eg.pid
	egProc := sup.eg.cmd.Process
	sup.mu.Unlock()

	if err := egProc.Kill(); err != nil {
		t.Fatalf("kill egress child: %v", err)
	}
	waitFor(t, "Armed=false after egress kill", func() bool {
		m, err := host.LoadManifest(id)
		return err == nil && !m.Armed
	})
	waitAlerts(t, gotAlerts, alerts.SandboxError)

	beforeAfterKill := readLookupCount(lookupLog)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	stDead, err := fg.sess.Open(ctx2, link.KindDNS, nil)
	cancel2()
	if err == nil {
		_, _ = stDead.Write(rawAllowed)
		_ = stDead.CloseWrite()
		deadBytes, _ := io.ReadAll(stDead)
		_ = stDead.Close()
		if len(deadBytes) != 0 {
			t.Fatalf("KindDNS after egress death returned %x, want empty fail-closed", deadBytes)
		}
	}
	sup.mu.Lock()
	curPID := sup.eg.pid
	dead := sup.eg.dead.Load()
	sup.mu.Unlock()
	if curPID != origPID || !dead || readLookupCount(lookupLog) != beforeAfterKill {
		t.Fatalf("after egress kill: pid=%d (orig %d) dead=%v lookups=%d (was %d)", curPID, origPID, dead, readLookupCount(lookupLog), beforeAfterKill)
	}
}
