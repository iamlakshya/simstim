//go:build darwin || linux

package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const agentModeHeapLimit = 4 << 20

type agentMemoryConfig struct {
	Root, ClientRoot, HostRoot, Binary, Binding, Transcript, Source, AgentPath string
	ClientControl, HostControl, NativeControl                                  string
	Forward                                                                    string
	HostID, KeyType, Fingerprint, BindingHash                                  string
	CertificateID                                                              string
	HostNames                                                                  []string
	CAType, CAFingerprint                                                      string
	Certificate, RequireRefusal, AllowRefusal, NativeGrammar                   bool
}

type agentMemorySample struct {
	HeapAlloc, HeapInuse, StackInuse, TotalAlloc uint64
	Goroutines                                   int
}

type agentMemoryProfile struct {
	LinkLiveBytes, FirewallLiveBytes, RelayLiveBytes                   int64
	LinkLiveObjects, FirewallLiveObjects, RelayLiveObjects             int64
	IdentityAllocBytes, NativePendingLive, NativePumpLive              int64
	NativeScanAlloc, CollectorAlloc, SealedBindingLive                 int64
	SealedBindingObjects                                               int64
	NativePumpFrames                                                   int
	ProjectionAllocBytes, SDKCertificateAllocBytes, SDKTupleAllocBytes int64
	SDKLargeTupleAllocBytes                                            int64
}

type agentMemoryEvent struct {
	Role, Event, Phase, Error   string
	Refused                     bool
	PID                         int
	Baseline, BeforeGC, AfterGC agentMemorySample
	Profile                     agentMemoryProfile
	PolicyChecks, Pings         int
}

type agentMemoryCommand struct {
	Command, Phase, Gate string
	Cycle                int
}

type agentMemoryControl struct {
	conn net.Conn
	dec  *json.Decoder
	enc  *json.Encoder
	mu   sync.Mutex
}

func agentMemoryConnect(path string) (*agentMemoryControl, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return &agentMemoryControl{conn: conn, dec: json.NewDecoder(bufio.NewReaderSize(conn, 4096)), enc: json.NewEncoder(conn)}, nil
}

func (c *agentMemoryControl) send(event agentMemoryEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	event.PID = os.Getpid()
	return c.enc.Encode(event)
}

func agentMemoryStats() agentMemorySample {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return agentMemorySample{m.HeapAlloc, m.HeapInuse, m.StackInuse, m.TotalAlloc, runtime.NumGoroutine()}
}

type agentMemoryMeter struct {
	base       agentMemorySample
	profile    []runtime.MemProfileRecord // Instrumentation allocated before the one baseline.
	goroutines []runtime.StackRecord
	oldGC      int
}

func newAgentMemoryMeter() *agentMemoryMeter {
	m := &agentMemoryMeter{profile: make([]runtime.MemProfileRecord, 4096), goroutines: make([]runtime.StackRecord, 256)}
	runtime.MemProfileRate = 1
	runtime.GC()
	runtime.GC()
	m.oldGC = debug.SetGCPercent(-1)
	m.base = agentMemoryStats()
	return m
}

func (m *agentMemoryMeter) sample(role, phase string) agentMemoryEvent {
	pre := agentMemoryStats()
	runtime.GC()
	runtime.GC()
	post := agentMemoryStats()
	e := agentMemoryEvent{Role: role, Event: "sample", Phase: phase, Baseline: m.base, BeforeGC: pre, AfterGC: post}
	for name, value := range map[string]agentMemorySample{"pre-GC": pre, "retained": post} {
		if int64(value.HeapAlloc)-int64(m.base.HeapAlloc) > agentModeHeapLimit {
			e.Error = fmt.Sprintf("%s %s heap growth exceeds the unchanged 4 MiB bound: %d", phase, name, int64(value.HeapAlloc)-int64(m.base.HeapAlloc))
		}
	}
	return e
}

func (m *agentMemoryMeter) profileObservation() (agentMemoryProfile, error) {
	runtime.GC()
	runtime.GC()
	n, ok := runtime.MemProfile(m.profile, true)
	if !ok || n > len(m.profile) {
		return agentMemoryProfile{}, errors.New("complete memory attribution exceeded fixed instrumentation capacity")
	}
	var p agentMemoryProfile
	gn, gok := runtime.GoroutineProfile(m.goroutines)
	if !gok || gn > len(m.goroutines) {
		return p, errors.New("goroutine attribution exceeded fixed instrumentation capacity")
	}
	for _, record := range m.goroutines[:gn] {
		frames := runtime.CallersFrames(record.Stack())
		for {
			frame, more := frames.Next()
			if strings.Contains(frame.Function, "/internal/client.(*agentNativeLog).pump") {
				p.NativePumpFrames++
				break
			}
			if !more {
				break
			}
		}
	}
	for _, record := range m.profile[:n] {
		if record.AllocObjects == 0 {
			continue
		}
		size := record.AllocBytes / record.AllocObjects
		var ring, fw, relay, identity, pending, pump, scan, capture, seal, projection, sdkCert, tuples bool
		frames := runtime.CallersFrames(record.Stack())
		for {
			frame, more := frames.Next()
			name := frame.Function
			ring = ring || strings.Contains(name, "/internal/link.(*buffer).write") || strings.Contains(name, "/internal/link.(*Stream).Write") || strings.Contains(name, "/internal/link.(*Session).onData")
			fw = fw || strings.Contains(name, "/internal/client.forwardAgentAuthentication")
			relay = relay || strings.Contains(name, "/internal/agentbridge.copyHalf")
			identity = identity || strings.Contains(name, "/internal/client.dial") || strings.Contains(name, "/internal/host.RunAgentBridge")
			pending = pending || strings.Contains(name, "/internal/client.makeAgentNativeLog")
			pump = pump || strings.Contains(name, "/internal/client.(*agentNativeLog).pump")
			scan = scan || strings.Contains(name, "/internal/client.parseAgentNativePeer")
			capture = capture || strings.Contains(name, "/internal/client.(*agentBindingCollector).accept")
			seal = seal || strings.Contains(name, "/internal/client.sealNativeAgentBinding")
			projection = projection || strings.Contains(name, "/internal/client.parseAgentBindingKey")
			sdkCert = sdkCert || strings.Contains(name, "golang.org/x/crypto/ssh.parseCert")
			tuples = tuples || strings.Contains(name, "golang.org/x/crypto/ssh.parseTuples")
			if !more {
				break
			}
		}
		live := record.InUseBytes()
		if projection {
			p.ProjectionAllocBytes += record.AllocBytes
		}
		if sdkCert {
			p.SDKCertificateAllocBytes += record.AllocBytes
		}
		if tuples {
			p.SDKTupleAllocBytes += record.AllocBytes
			if size >= 1024 {
				p.SDKLargeTupleAllocBytes += record.AllocBytes
			}
		}
		if ring && size >= 65536 {
			p.LinkLiveBytes += live
			p.LinkLiveObjects += record.InUseObjects()
		}
		if fw && size >= 32768 {
			p.FirewallLiveBytes += live
			p.FirewallLiveObjects += record.InUseObjects()
		}
		if relay && size >= 32768 {
			p.RelayLiveBytes += live
			p.RelayLiveObjects += record.InUseObjects()
		}
		if identity && size == 16384 {
			p.IdentityAllocBytes += record.AllocBytes
		}
		if pending && size == 65536 {
			p.NativePendingLive += live
		}
		if pump && size == 32768 {
			p.NativePumpLive += live
		}
		if scan && size >= 65536 {
			p.NativeScanAlloc += record.AllocBytes
		}
		if capture && size >= 262148 {
			p.CollectorAlloc += record.AllocBytes
		}
		if seal && size >= 262148 {
			p.SealedBindingLive += live
			p.SealedBindingObjects += record.InUseObjects()
		}
	}
	return p, nil
}

func (m *agentMemoryMeter) attribution(role string) (agentMemoryProfile, error) {
	p, err := m.profileObservation()
	if err != nil {
		return p, err
	}
	if p.LinkLiveBytes != 32*65536 || p.LinkLiveObjects != 32 || p.IdentityAllocBytes < 16384 {
		return p, errors.New("full 16-stream bidirectional link and identity residency was not reached")
	}
	// The compiler may keep the fixed pump buffer on its live goroutine
	// stack. A full transcript forces its use. StackInuse is reported as a
	// separate process metric and is not precise per-pump byte attribution.
	pumpResident := p.NativePumpLive == 32768 || p.NativePumpLive == 0 && p.NativePumpFrames == 1
	if role == "client" && (p.FirewallLiveBytes != 16*32768 || p.FirewallLiveObjects != 16 || p.NativePendingLive != 65536 || !pumpResident || p.NativeScanAlloc < 65536 || p.CollectorAlloc < 262148 || p.SealedBindingObjects != 1 || p.ProjectionAllocBytes == 0 || p.SDKCertificateAllocBytes == 0 || p.SDKLargeTupleAllocBytes != 0) {
		return p, errors.New("client firewall/native startup/shared-binding residency was incomplete")
	}
	if role == "host" && (p.RelayLiveBytes != 32*32768 || p.RelayLiveObjects != 32) {
		return p, errors.New("the host's 32 relay buffers were not resident")
	}
	return p, nil
}

func (m *agentMemoryMeter) rejectionAttribution() (agentMemoryProfile, error) {
	p, err := m.profileObservation()
	if err != nil {
		return p, err
	}
	if p.CollectorAlloc < 262148 || p.SDKCertificateAllocBytes != 0 || p.SDKTupleAllocBytes != 0 || p.FirewallLiveObjects != 0 || p.LinkLiveObjects != 0 {
		return p, errors.New("nested CA was not captured and refused before the SDK or agent streams")
	}
	return p, nil
}

func agentMemoryReadConfig(path string) (agentMemoryConfig, error) {
	var cfg agentMemoryConfig
	file, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer file.Close()
	err = json.NewDecoder(io.LimitReader(file, 16384)).Decode(&cfg)
	return cfg, err
}

// Child modes are test-only contract/resource fixtures. The native image
// shim is deliberately NOT evidence of native OpenSSH provenance.
func init() {
	role := os.Getenv("AGENT_MODE_MEMORY_ROLE")
	if role == "" {
		return
	}
	cfg, err := agentMemoryReadConfig(os.Getenv("AGENT_MODE_MEMORY_CONFIG"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "memory fixture config:", err)
		os.Exit(91)
	}
	if role == "client" && filepath.Base(os.Args[0]) == "ssh" {
		role = "native"
	}
	switch role {
	case "client":
		err = runAgentMemoryClient(cfg)
	case "host":
		err = runAgentMemoryHost(cfg)
	case "native":
		err = runAgentMemoryNative(cfg)
	default:
		err = errors.New("unknown memory fixture role")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent memory fixture:", err)
		os.Exit(92)
	}
	os.Exit(0)
}
