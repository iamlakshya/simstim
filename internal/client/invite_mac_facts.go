package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const joinedMacFactsActionV1 = "facts-v1"
const joinedMacFactsLimitV1 = 32 << 10

// ReadJoinedMacFactsV1 runs only the fixed facts action on the nearest pinned
// Mac hop of this same live saved connection. JSON is bounded advisory data;
// it cannot create a new Source, provider, root or sandbox permission.
func ReadJoinedMacFactsV1(ctx context.Context, original *Conn) (_ []byte, returned error) {
	if ctx == nil {
		return nil, hostStateRemovalError("Mac facts need an active original caller context")
	}
	if grantRequestOf(ctx) != nil || ctx.Value(hostTransportAttemptKey{}) != nil || ctx.Value(hostStateObservationKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(savedHostRemovalPhaseKeyV1{}) != nil {
		return nil, hostStateRemovalError("Mac facts cannot borrow another operation's grant or removal purpose")
	}
	if original == nil || original.agentOnly || original.hostTransport == nil || original.hostTransport.kind != hostTransportSaved || original.HostIDChanged() || !ValidHostID(original.HostID()) {
		return nil, hostStateRemovalError("Mac facts need the original live saved connection")
	}
	if err := original.CheckSavedSource(ctx); err != nil {
		return nil, err
	}
	var target *HostPrepareTargetV1
	err := withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := original.hostTransport.checkConn(original); err != nil {
			return err
		}
		if err := original.hostTransport.source.check(locked); err != nil {
			return err
		}
		hosts, err := LoadHosts()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == original.Host.Name })
		if index < 0 || !SameEntry(hosts[index], original.Host) {
			return hostStateRemovalError("the original facts connection's saved entry changed")
		}
		target, err = captureHostPrepareTargetLocked(locked, original.Host, original.hostTransport.source, false)
		if err != nil {
			return err
		}
		return target.checkLocked(locked, original.HostID())
	})
	if err != nil {
		return nil, err
	}
	if target.host.JoinedSSH == nil || !validJoinedRecord(target.host.JoinedSSH) {
		return nil, joinedFactsErrorV1("this host has no original joined Mac path")
	}
	origin, err := openJoinedSSHOriginV1(target.Host(), target.HostID(), false)
	if err != nil {
		return nil, err
	}
	restriction, err := openJoinedRouteRestrictionV1(ctx, origin.manifest.Route)
	if err != nil {
		return nil, errors.Join(err, origin.close())
	}
	lease := &joinedFactsLeaseV1{connection: original, target: target, origin: origin, routeRestriction: restriction}
	defer func() { returned = errors.Join(returned, lease.Close()) }()
	if len(origin.manifest.Hops) == 0 {
		return nil, nil
	}
	if err = lease.Revalidate(ctx); err != nil {
		return nil, err
	}
	final := target.Host()
	hop := cloneObservationHost(final)
	hop.SSH = final.SSH + "-hop-1"
	native, err := (&SSH{Host: hop, origin: &final, ownConnection: true}).WithNativeSource(&joinedFactsSourceV1{lease: lease})
	if err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	nctx, finishNative, err := native.beginNativeSource(bounded)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, finishNative()) }()
	start, release, err := native.beginSSHStart(nctx, "read joined Mac facts")
	if err != nil {
		return nil, err
	}
	defer release()
	if err = lease.Revalidate(start); err != nil {
		return nil, err
	}
	wrapper, err := writeWrapperContext(native.policyContext(start), hop)
	if err != nil {
		return nil, err
	}
	if err = lease.Revalidate(start); err != nil {
		return nil, err
	}
	// The normal final-host pin must not be donated to the outer hop. This
	// exact pin file and alias came from the same original accepted path.
	pins, err := pinArgsContext(start, final.Name, filepath.Join(origin.directory, "hop-1.keys"))
	if err != nil {
		return nil, err
	}
	if len(pins) < 2 {
		return nil, joinedFactsErrorV1("the nearest-hop pin options are incomplete")
	}
	pins[1] = "HostKeyAlias=" + hop.SSH
	if err = lease.Revalidate(start); err != nil {
		return nil, err
	}
	args := slices.Concat([]string{"-F", wrapper}, NoForwarding(), NoPortForwards(), OwnConnection(), pins, []string{"-o", "BatchMode=yes", "-o", "PreferredAuthentications=publickey", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-T", "--", hop.SSH, joinedMacFactsActionV1})
	command := exec.CommandContext(start, "ssh", args...)
	command.WaitDelay = sshWaitDelay
	var stdout, stderr joinedFactsBufferV1
	stdout.limit = joinedMacFactsLimitV1
	stderr.limit = 2048
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err = lease.Revalidate(start); err != nil {
		return nil, err
	}
	runErr := command.Start()
	release()
	if runErr == nil {
		runErr = command.Wait()
	}
	if err = lease.Revalidate(bounded); err != nil {
		return nil, errors.Join(err, runErr)
	}
	if runErr != nil {
		return nil, joinedFactsErrorV1("the fixed outer Mac facts action did not finish")
	}
	if stdout.exceeded {
		return nil, joinedFactsErrorV1("the Mac facts reply exceeds 32 KiB")
	}
	if err = validateJoinedMacFactsV1(stdout.Bytes()); err != nil {
		return nil, err
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

type joinedFactsBufferV1 struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *joinedFactsBufferV1) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(data)
}

type joinedFactsSourceV1 struct{ lease *joinedFactsLeaseV1 }

func (*joinedFactsSourceV1) joinedProjectionV1() {}

func (s *joinedFactsSourceV1) OpenSSHNativeSource(ctx context.Context) (SSHNativeSourceLease, error) {
	if s == nil || s.lease == nil {
		return nil, joinedFactsErrorV1("the original facts lease is missing")
	}
	if err := s.lease.Revalidate(ctx); err != nil {
		return nil, err
	}
	return &joinedFactsBorrowV1{source: s.lease}, nil
}

type joinedFactsBorrowV1 struct {
	source *joinedFactsLeaseV1
	closed bool
}

func (b *joinedFactsBorrowV1) Revalidate(ctx context.Context) error {
	if b == nil || b.closed || b.source == nil {
		return joinedFactsErrorV1("the original facts borrow ended")
	}
	return b.source.Revalidate(ctx)
}
func (b *joinedFactsBorrowV1) Close() error { b.closed = true; return nil }

type joinedFactsLeaseV1 struct {
	connection       *Conn
	target           *HostPrepareTargetV1
	origin           *joinedSSHRemovalV1
	routeRestriction *joinedRouteRestrictionV1
	closed           bool
}

func (s *joinedFactsLeaseV1) Revalidate(ctx context.Context) error {
	if s == nil || s.closed || s.connection == nil || s.target == nil || s.origin == nil {
		return joinedFactsErrorV1("the original facts Source ended")
	}
	if err := s.connection.CheckSavedSource(ctx); err != nil {
		return err
	}
	if err := s.target.Check(ctx); err != nil {
		return err
	}
	if s.connection.HostID() != s.target.HostID() || !sameHostTransportSource(s.connection.hostTransport.source, s.target.source) {
		return hostStateRemovalError("the facts route changed its original saved Source")
	}
	if err := s.origin.checkRetained(); err != nil {
		return err
	}
	return s.routeRestriction.check(ctx)
}
func (s *joinedFactsLeaseV1) Close() error {
	if s == nil || s.closed {
		return nil
	}
	err := errors.Join(s.origin.close(), s.routeRestriction.close())
	if err == nil {
		s.closed = true
	}
	return err
}
func joinedFactsErrorV1(message string) error {
	return &UserError{Msg: message, Hint: "Check the original Mac's join and root-owned facts dispatcher; its facts remain unknown.", Code: "mac_facts_unknown", Kind: KindFailed}
}

// The route returns one strict, bounded object. Its fields remain display data.
func validateJoinedMacFactsV1(body []byte) error {
	if len(body) > joinedMacFactsLimitV1 {
		return joinedFactsErrorV1("the Mac facts reply exceeds 32 KiB")
	}
	var facts struct {
		Version              int    `json:"v"`
		Kind                 string `json:"kind"`
		MacOS                string `json:"macos"`
		Chip                 string `json:"chip"`
		OnBattery            bool   `json:"on_battery"`
		OnBatteryKnown       bool   `json:"on_battery_known"`
		BatteryPercent       int    `json:"battery_percent"`
		BatteryKnown         bool   `json:"battery_known"`
		ACSleepMinutes       int    `json:"ac_sleep_minutes"`
		ACSleepKnown         bool   `json:"ac_sleep_known"`
		MDMEnrolled          bool   `json:"mdm_enrolled"`
		MDMKnown             bool   `json:"mdm_known"`
		SSHDManaged          bool   `json:"sshd_managed"`
		SSHDManagedKnown     bool   `json:"sshd_managed_known"`
		ThermalWarning       bool   `json:"thermal_warning"`
		ThermalKnown         bool   `json:"thermal_known"`
		SleepPrevented       bool   `json:"sleep_prevented"`
		SleepAssertionsKnown bool   `json:"sleep_assertions_known"`
		StartMode            string `json:"start_mode"`
		LaunchEnabled        bool   `json:"launch_enabled"`
		LaunchKnown          bool   `json:"launch_known"`
	}
	if err := checkJoinedFactsObjectV1(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&facts); err != nil {
		return joinedFactsErrorV1("the fixed Mac facts reply is malformed")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return joinedFactsErrorV1("the Mac facts reply contains more than one object")
	}
	if facts.Version != 1 || facts.Kind != "mac" || facts.StartMode != "daemon" && facts.StartMode != "user-agent" || facts.BatteryPercent < 0 || facts.BatteryPercent > 100 || facts.ACSleepMinutes < 0 || facts.ACSleepMinutes > 1<<31-1 {
		return joinedFactsErrorV1("the Mac facts fields are outside their closed v1 bounds")
	}
	for _, value := range []string{facts.MacOS, facts.Chip} {
		if len(value) > 256 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return joinedFactsErrorV1("the Mac facts strings are invalid")
		}
	}
	return nil
}

func checkJoinedFactsObjectV1(body []byte) error {
	allowed := map[string]bool{}
	for _, name := range []string{"v", "kind", "macos", "chip", "on_battery", "on_battery_known", "battery_percent", "battery_known", "ac_sleep_minutes", "ac_sleep_known", "mdm_enrolled", "mdm_known", "sshd_managed", "sshd_managed_known", "thermal_warning", "thermal_known", "start_mode", "launch_enabled", "launch_known", "sleep_prevented", "sleep_assertions_known"} {
		allowed[name] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return joinedFactsErrorV1("the Mac facts reply is not one object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || seen[name] {
			return joinedFactsErrorV1("the Mac facts reply has an unknown or repeated field")
		}
		seen[name] = true
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return joinedFactsErrorV1("the Mac facts field is malformed")
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return joinedFactsErrorV1("the Mac facts object is incomplete")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return joinedFactsErrorV1("the Mac facts reply has trailing data")
	}
	return nil
}
