package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type prepareTestProc struct {
	stdin  io.WriteCloser
	stdout io.Reader
	done   chan struct{}
	once   sync.Once
}

func (p *prepareTestProc) Stdin() io.WriteCloser { return p.stdin }
func (p *prepareTestProc) Stdout() io.Reader     { return p.stdout }
func (p *prepareTestProc) Stderr() string        { return "" }
func (p *prepareTestProc) Wait() error           { <-p.done; return nil }
func (p *prepareTestProc) Kill()                 { p.once.Do(func() { _ = p.stdin.Close(); close(p.done) }) }

type prepareTestTransport struct {
	starts atomic.Int32
	serve  func(ctx context.Context) (Proc, error)
}

func (t *prepareTestTransport) Start(ctx context.Context, _ string) (Proc, error) {
	t.starts.Add(1)
	return t.serve(ctx)
}
func (t *prepareTestTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("unexpected Run on bridge test transport")
}
func (t *prepareTestTransport) ReachHint() string { return "test-transport" }

// newPrepareBridgeTransport speaks real proto frames over io.Pipe for dial/Call.
func newPrepareBridgeTransport(t *testing.T, hostID string, features []string, handle func(proto.Request, *proto.Writer) (any, *proto.Error, bool)) *prepareTestTransport {
	t.Helper()
	return &prepareTestTransport{serve: func(ctx context.Context) (Proc, error) {
		serverIn, clientOut := io.Pipe()
		clientIn, serverOut := io.Pipe()
		proc := &prepareTestProc{stdin: clientOut, stdout: clientIn, done: make(chan struct{})}
		t.Cleanup(func() {
			proc.Kill()
			_ = serverIn.Close()
			_ = serverOut.Close()
			_ = clientIn.Close()
		})
		go func() {
			defer func() {
				_ = serverOut.Close()
				_ = serverIn.Close()
				proc.Kill()
			}()
			r := bufio.NewReader(serverIn)
			w := proto.NewWriter(serverOut)
			f, err := proto.Read(r)
			if err != nil || f.Type != proto.THello {
				return
			}
			if err := w.WriteJSON(proto.THello, proto.Hello{
				Magic:    proto.Magic,
				Protocol: version.Protocol,
				Version:  version.Version,
				Role:     "bridge",
				HostID:   hostID,
				Features: features,
			}); err != nil {
				return
			}
			for {
				frame, err := proto.Read(r)
				if err != nil {
					return
				}
				if frame.Type != proto.TRequest {
					continue
				}
				var req proto.Request
				if err := json.Unmarshal(frame.Payload, &req); err != nil {
					return
				}
				val, perr, closeNow := handle(req, w)
				if closeNow {
					return
				}
				resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
				if perr == nil && val != nil {
					if raw, ok := val.(json.RawMessage); ok {
						resp.Result = raw
					} else {
						b, merr := json.Marshal(val)
						if merr != nil {
							return
						}
						resp.Result = b
					}
				}
				if err := w.WriteJSON(proto.TResponse, resp); err != nil {
					return
				}
			}
		}()
		return proc, nil
	}}
}

func prepareTestHostKey(seed byte) hostKey {
	typ := "ssh-ed25519"
	blob := make([]byte, 4+len(typ)+36)
	binary.BigEndian.PutUint32(blob[:4], uint32(len(typ)))
	copy(blob[4:], typ)
	binary.BigEndian.PutUint32(blob[4+len(typ):8+len(typ)], 32)
	for i := range 32 {
		blob[8+len(typ)+i] = seed + byte(i)
	}
	return hostKey{Type: typ, Blob: blob}
}

func prepareGVisorProbe() api.CapProbe {
	return api.CapProbe{
		OS:          "linux",
		Arch:        "amd64",
		Kernel:      "6.8.0",
		User:        "dev",
		UID:         1000,
		UserNS:      true,
		LandlockABI: 3,
		Seccomp:     true,
		MemTotal:    8 << 30,
		Walls: []api.WallProbe{
			{Driver: "process", Level: "process", OK: true},
			{Driver: "gvisor", Level: "gvisor", OK: true},
		},
	}
}

func TestHostPrepareRegistrationCommitToSavedContinuationAndFloorEstablish(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "macguest", SSH: "macguest.example"}
	hostID := strings.Repeat("a", 32)
	key := prepareTestHostKey(1)

	staged, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	st, err := stageForContext(staged, h.Name)
	if err != nil || st == nil {
		t.Fatalf("missing owned stage: %v", err)
	}
	if err := writePin(h.Name, st, []hostKey{key}); err != nil {
		t.Fatal(err)
	}
	change := HostChange{Kind: "host add", Now: &h, Pin: true, Tier: TierOwn, HostID: hostID}
	obs, err := ObserveHostRegistrationContext(staged, change)
	if err != nil {
		t.Fatal(err)
	}
	regCtx := WithHostStateObservation(staged, obs)
	regCtx, unlockReg, err := LockHost(regCtx, h.Name, "host add")
	if err != nil {
		t.Fatal(err)
	}
	features := []string{"sandbox", api.FeatureSandboxPrepareV1}
	var prepareCalls atomic.Int32
	tr := newPrepareBridgeTransport(t, hostID, features, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		switch req.Op {
		case "sandbox.probe":
			return prepareGVisorProbe(), nil, false
		case "sandbox.prepare":
			prepareCalls.Add(1)
			var args api.SandboxPrepareArgsV1
			if err := json.Unmarshal(req.Args, &args); err != nil {
				return nil, &proto.Error{Code: "bad_args", Message: err.Error()}, false
			}
			return api.SandboxPrepareResultV1{Version: 1, Level: args.Level, Driver: args.Level, BootMS: 42}, nil, false
		default:
			return nil, &proto.Error{Code: "unknown", Message: "unexpected op " + req.Op}, false
		}
	})
	regConn, err := dial(regCtx, h, route{t: tr})
	if err != nil {
		unlockReg()
		t.Fatal(err)
	}
	defer regConn.Close()
	if regConn.hostTransport == nil || regConn.hostTransport.kind != hostTransportRegistration {
		unlockReg()
		t.Fatal("pre-save dial did not stay in registration transport class")
	}

	target, err := UpdateHostsForPreparation(regCtx, change)
	unlockReg()
	abort()
	if err != nil {
		t.Fatal(err)
	}
	if !target.FromRegistration() || target.Name() != h.Name || target.Tier() != TierOwn || target.HostID() != hostID {
		t.Fatalf("unexpected committed target: reg=%v name=%q tier=%v id=%q", target.FromRegistration(), target.Name(), target.Tier(), target.HostID())
	}
	storedPin, err := PinOf(h.Name)
	if err != nil || storedPin.Fingerprint != key.Fingerprint() || target.key != key.Fingerprint() {
		t.Fatalf("registration lost the exact native pin before preparation: pin=%+v target=%q err=%v", storedPin, target.key, err)
	}
	storedKey, err := recordedTierKey(h.Name)
	if err != nil || storedKey != key.Fingerprint() {
		t.Fatalf("registration lost its recorded full fingerprint: key=%q err=%v", storedKey, err)
	}

	// Old pre-save registration connection cannot promote to saved preparation.
	reqBad := NewHostPrepareGrantRequest()
	defer reqBad.Close()
	if _, err := regConn.PrepareSandboxV1(WithGrantRequest(t.Context(), reqBad), target, HostPrepareOptionsV1{}); err == nil {
		t.Fatal("pre-save registration connection promoted to saved sandbox preparation")
	}
	if prepareCalls.Load() != 0 {
		t.Fatal("rejected registration connection reached sandbox.prepare on the wire")
	}

	// Open a fresh saved connection from the writer-derived target.
	savedConn, err := target.dialRoute(t.Context(), route{t: tr})
	if err != nil {
		t.Fatal(err)
	}
	defer savedConn.Close()
	if savedConn.hostTransport == nil || savedConn.hostTransport.kind != hostTransportSaved {
		t.Fatal("target dial did not produce a saved transport stamp")
	}

	req := NewHostPrepareGrantRequest()
	defer req.Close()
	prepCtx := WithGrantRequest(t.Context(), req)
	outcome, err := savedConn.PrepareSandboxV1(prepCtx, target, HostPrepareOptionsV1{})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Tested() || outcome.NoRunnableWall() || outcome.ObservedLevel() != "gvisor" || outcome.Result().Driver != "gvisor" || outcome.Result().BootMS != 42 {
		t.Fatalf("unexpected prepare outcome: tested=%v level=%q res=%+v", outcome.Tested(), outcome.ObservedLevel(), outcome.Result())
	}
	if prepareCalls.Load() != 1 {
		t.Fatalf("expected 1 sandbox.prepare call, got %d", prepareCalls.Load())
	}

	saved, err := RecordSandboxPreparationV1(prepCtx, target, outcome)
	if err != nil {
		t.Fatal(err)
	}
	if saved.WallFloor != "gvisor" || saved.Prepared == nil || saved.Prepared.Level != "gvisor" || saved.Prepared.Arch != "amd64" || saved.Prepared.MemTotal != 8<<30 {
		t.Fatalf("unexpected recorded host: floor=%q prepared=%+v", saved.WallFloor, saved.Prepared)
	}
	reloaded, err := FindHost(h.Name)
	if err != nil || !SameEntry(reloaded, saved) {
		t.Fatalf("hosts.toml did not persist exact recorded entry: %v", err)
	}
	// Replaying the same outcome after commit must fail closed.
	if _, err := RecordSandboxPreparationV1(prepCtx, target, outcome); err == nil {
		t.Fatal("replaying an already recorded preparation outcome succeeded")
	}
}

func TestUpdateHostsForPreparationFailureOrForgetReturnsNoTarget(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if target, err := UpdateHostsForPreparation(t.Context(), HostChange{Kind: "host rm", Was: &h}); err == nil || target != nil {
		t.Fatal("forget returned a preparation target")
	}
	if _, err := FindHost(h.Name); err != nil {
		t.Fatal("refused forget removed the saved host")
	}

	wrongWas := Host{Name: "box", SSH: "other.example"}
	if target, err := UpdateHostsForPreparation(t.Context(), HostChange{Kind: "host set", Was: &wrongWas, Now: &h}); err == nil || target != nil {
		t.Fatal("CAS mismatch returned a preparation target")
	}

	prevWrite := writeHosts
	writeHosts = func([]Host) error { return errors.New("simulated write error") }
	updated := h
	updated.Labels = map[string]string{"k": "v"}
	target, err := UpdateHostsForPreparation(t.Context(), HostChange{Kind: "host set", Was: &h, Now: &updated})
	writeHosts = prevWrite
	if err == nil || target != nil {
		t.Fatal("failed writeHosts returned a preparation target")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if target, err := UpdateHostsForPreparation(canceled, HostChange{Kind: "host set", Was: &h, Now: &updated}); err == nil || target != nil {
		t.Fatal("canceled context returned a preparation target")
	}
}

func TestHostPrepareTargetRejectsRemoveReaddABAAndForgedTargets(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "box.example"}
	hostID := strings.Repeat("b", 32)
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierOwn, hostID, ""); err != nil {
		t.Fatal(err)
	}
	target, err := OpenHostPrepareTargetV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}

	tr := newPrepareBridgeTransport(t, hostID, []string{"sandbox", api.FeatureSandboxPrepareV1}, func(req proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		if req.Op == "sandbox.probe" {
			return prepareGVisorProbe(), nil, false
		}
		if req.Op == "sandbox.prepare" {
			return api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 19}, nil, false
		}
		return nil, &proto.Error{Code: "unknown", Message: req.Op}, false
	})
	conn, err := target.dialRoute(t.Context(), route{t: tr})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := NewHostPrepareGrantRequest()
	defer req.Close()
	prepCtx := WithGrantRequest(t.Context(), req)
	outcome, err := conn.PrepareSandboxV1(prepCtx, target, HostPrepareOptionsV1{})
	if err != nil {
		t.Fatal(err)
	}

	// Identical remove and re-add rotates # membership-generation (ABA).
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := target.Check(t.Context()); err == nil {
		t.Fatal("stale target accepted identical remove/re-add ABA")
	}
	beforeStarts := tr.starts.Load()
	if _, err := target.dialRoute(t.Context(), route{t: tr}); err == nil || tr.starts.Load() != beforeStarts {
		t.Fatal("stale target dialed after remove/re-add ABA")
	}
	if _, err := RecordSandboxPreparationV1(prepCtx, target, outcome); err == nil {
		t.Fatal("RecordSandboxPreparationV1 wrote floor after remove/re-add ABA")
	}
	reloaded, err := FindHost(h.Name)
	if err != nil || reloaded.WallFloor != "" || reloaded.Prepared != nil {
		t.Fatalf("ABA refusal modified saved host: floor=%q prepared=%+v err=%v", reloaded.WallFloor, reloaded.Prepared, err)
	}

	// Forged raw struct targets and display-only outcomes must never authorise writes.
	forgedTarget := &HostPrepareTargetV1{host: h, tier: TierOwn, hostID: hostID}
	if err := forgedTarget.Check(t.Context()); err == nil {
		t.Fatal("forged HostPrepareTargetV1 passed Check")
	}
	freshTarget, err := OpenHostPrepareTargetV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	forgedOutcome := &SandboxPrepareOutcomeV1{
		tested:        true,
		observedLevel: "gvisor",
		result:        api.SandboxPrepareResultV1{Version: 1, Level: "gvisor", Driver: "gvisor", BootMS: 5},
	}
	if _, err := RecordSandboxPreparationV1(prepCtx, freshTarget, forgedOutcome); err == nil {
		t.Fatal("display-only forged outcome authorised floor publication")
	}
}

func TestHostPrepareOpenAndDialRefuseInvalidContextAndIDChanged(t *testing.T) {
	stateFDTestHome(t)
	if _, err := OpenHostPrepareTargetV1(t.Context(), "missing"); err == nil {
		t.Fatal("OpenHostPrepareTargetV1 succeeded for missing host")
	}
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierOwn, strings.Repeat("a", 32), ""); err != nil {
		t.Fatal(err)
	}
	target, err := OpenHostPrepareTargetV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}

	// Dial refuses carried host lock or registration observation.
	lockedCtx, unlock, err := LockHost(t.Context(), h.Name, "other op")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	tr := newPrepareBridgeTransport(t, strings.Repeat("a", 32), []string{"sandbox", api.FeatureSandboxPrepareV1}, func(proto.Request, *proto.Writer) (any, *proto.Error, bool) {
		return nil, nil, false
	})
	if _, err := target.dialRoute(lockedCtx, route{t: tr}); err == nil || tr.starts.Load() != 0 {
		t.Fatal("target dial ran inside a carried host lock")
	}
	unlock()

	// IDChanged host cannot be opened for preparation.
	ts, err := readTiersFile()
	if err != nil {
		t.Fatal(err)
	}
	entry := ts.entries[h.Name]
	entry.IDChanged = true
	ts.entries[h.Name] = entry
	if err := writeTiersFile(ts); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenHostPrepareTargetV1(t.Context(), h.Name); err == nil {
		t.Fatal("OpenHostPrepareTargetV1 opened a host with IDChanged=true")
	}
	if err := target.Check(t.Context()); err == nil {
		t.Fatal("existing target stayed valid after IDChanged=true")
	}
}
