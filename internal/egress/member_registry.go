package egress

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

const legacyExposedIdentity = "legacy-exposed-v1"

type registeredSecret struct {
	value      api.Secret
	identity   string
	valueEpoch string
	mode       api.SecretMode
	present    bool
	bound      *boundSecret
	history    []api.Secret
	minted     []registeredMint
}

type memberState struct {
	version int
	// metadata owns the policy and wire fields. Secrets is always nil here.
	metadata       InitArgs
	kinds          []string
	secrets        map[string]registeredSecret
	generation     string
	echo           map[string]*responseEchoGroupSnapshot
	requestEntries []requestBoundSecret
	requestView    *requestSecretSnapshot
	sourceKinds    map[string][]string
	retired        map[string][]registeredRetired
	decoys         []api.DecoyTripwire
	decoyClasses   []compiledRule
}

type preparedInitial struct {
	state      *memberState
	engine     *Engine
	redaction  *auditSecrets
	dnsMode    string
	secretOnly bool
}

func (registeredSecret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
func (memberState) Format(f fmt.State, _ rune)      { _, _ = io.WriteString(f, api.Redacted) }
func (preparedInitial) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, api.Redacted) }

func memberError(code, message, hint string) *proto.Error {
	return &proto.Error{Code: code, Message: message, Hint: hint}
}

func validRegistryName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range []byte(name) {
		letter := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func checkRegistryValue(name string, value api.Secret) error {
	if !validRegistryName(name) {
		return memberError("bad_secret", "invalid environment name for a secret", "Use letters, digits and underscores, starting with a letter or underscore.")
	}
	if name == "TERM" || name == "COLORTERM" || name == "TERM_PROGRAM" || name == "TERM_PROGRAM_VERSION" || strings.HasPrefix(name, brand.EnvPrefix) {
		return memberError("bad_secret", "the session reserves secret name "+name, "Rename this secret before starting the sandbox.")
	}
	if strings.ContainsRune(string(value), 0) {
		return memberError("bad_secret", "secret "+name+" is not an environment value", "Remove the NUL byte from its approved command's output.")
	}
	return nil
}

// initialArgs projects the complete current state into the legacy exposed
// initial wire. Future binding forms must extend the negotiated registry,
// never fall through this adapter into a real guest value.
func (state *memberState) initialArgs() (InitArgs, error) {
	if state.version != MemberRegistryV1 {
		return InitArgs{}, memberError("unsupported_member_registry", "current secrets need a newer registry protocol", "Use matching helpers for the saved secret bindings before adding a member.")
	}
	a := state.metadata
	a.AgentKinds = slices.Clone(a.AgentKinds)
	a.Decoys = api.CloneDecoyTripwires(state.decoys)
	a.Secrets = make(map[string]string, len(state.secrets))
	for name, secret := range state.secrets {
		if secret.identity != legacyExposedIdentity {
			return InitArgs{}, memberError("unsupported_binding", "secret "+name+" needs a newer binding protocol", "Use matching helpers with bound-secret support, or start a fresh sandbox with approved exposed values.")
		}
		a.Secrets[name] = string(secret.value)
	}
	return a, nil
}

// prepareInitialState is the one admission validator for init, replacement
// and membership. It constructs every fallible prospective part before the
// shared ledger, engine, registry or live scrubber is touched.
func prepareInitialState(a InitArgs) (*preparedInitial, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, memberError("bad_request", "invalid egress initial state", "Check the saved policy and retry with matching helpers.")
	}
	var owned InitArgs
	if json.Unmarshal(raw, &owned) != nil {
		return nil, memberError("bad_request", "invalid egress initial state", "Check the saved policy and retry with matching helpers.")
	}
	kinds, err := canonicalAgentKinds(owned.AgentKinds)
	if err != nil {
		return nil, err
	}
	if _, known := agents.Find(owned.Agent); known {
		kinds = append(kinds, owned.Agent)
		kinds, err = canonicalAgentKinds(kinds)
		if err != nil {
			return nil, err
		}
	}
	// The original single Agent keeps its existing meaning, including an
	// unknown legacy kind with no builtin. Do not duplicate it in the wire.
	owned.AgentKinds = nil
	for _, kind := range kinds {
		if kind != owned.Agent {
			owned.AgentKinds = append(owned.AgentKinds, kind)
		}
	}
	decoys, classes, err := prepareDecoyInput(owned.Decoys, owned.Policy)
	if err != nil {
		return nil, err
	}
	state := &memberState{version: MemberRegistryV1, metadata: owned, kinds: kinds, secrets: make(map[string]registeredSecret, len(owned.Secrets)), decoys: decoys, decoyClasses: classes}
	for name, value := range owned.Secrets {
		secret := api.Secret(value)
		if err := checkRegistryValue(name, secret); err != nil {
			return nil, err
		}
		state.secrets[name] = registeredSecret{value: secret, identity: legacyExposedIdentity}
	}
	state.metadata.Secrets = nil
	state.metadata.Decoys = nil
	complete, err := state.initialArgs()
	if err != nil {
		return nil, err
	}
	raw, err = json.Marshal(complete)
	if err != nil {
		return nil, memberError("bad_request", "invalid egress initial state", "Check the saved policy and retry with matching helpers.")
	}
	// Use the transport's actual size check, including JSON request and
	// type-byte overhead. Its four-byte length prefix is outside MaxFrame.
	if err := proto.NewWriter(io.Discard).WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: CtlInit, Args: raw}); err != nil {
		return nil, memberError("state_too_large", "combined egress state exceeds the control frame limit", "Start a fresh sandbox with only the required secret names and policy rules.")
	}
	mode, err := ParseDNSMode(owned.DNS)
	if err != nil {
		return nil, memberError("bad_request", "invalid DNS policy mode", "Use an empty, real or synthetic DNS mode.")
	}
	engine, err := NewEngineForAgents(kinds, owned.Policy)
	if err != nil {
		code := "bad_request"
		var pe *proto.Error
		if errors.As(err, &pe) {
			code = pe.Code
		}
		return nil, memberError(code, "invalid egress network policy", "Check the network rules, methods and complete layer order before retrying.")
	}
	if len(state.decoys) != 0 {
		view, err := newRequestSecretSnapshot(nil)
		if err != nil {
			return nil, err
		}
		state.requestView, err = view.withDecoyTripwires(state.decoys)
		if err != nil {
			return nil, err
		}
	}
	scrubber := NewScrubber(decoyScrubValues(complete.Secrets, state.decoys))
	return &preparedInitial{state: state, engine: engine, redaction: scrubber.snapshot(), dnsMode: mode}, nil
}

func (state *memberState) memberCandidate(a MemberAddArgs) (InitArgs, map[string]api.Secret, []string, error) {
	if a.Version != MemberRegistryV1 {
		return InitArgs{}, nil, nil, memberError("unsupported_member_registry", "unsupported member registry version", "Update the supervisor and egress helper together before adding this member.")
	}
	if a.Kind != "" {
		if _, err := canonicalAgentKinds([]string{a.Kind}); err != nil {
			return InitArgs{}, nil, nil, err
		}
	}
	names := make([]string, 0, len(a.ExposedValues))
	for name, value := range a.ExposedValues {
		if err := checkRegistryValue(name, value); err != nil {
			return InitArgs{}, nil, nil, err
		}
		if old, exists := state.secrets[name]; exists && (old.identity != legacyExposedIdentity || old.value != value) {
			return InitArgs{}, nil, nil, memberError("secret_change_unsupported", "secret "+name+" already has a different value or binding", "Start a fresh sandbox with the intended secrets. Live rotation and rebinding are not available.")
		}
		names = append(names, name)
	}
	slices.Sort(names)
	next, err := state.initialArgs()
	if err != nil {
		return InitArgs{}, nil, nil, err
	}
	next.AgentKinds = slices.Clone(state.kinds)
	if a.Kind != "" {
		next.AgentKinds = append(next.AgentKinds, a.Kind)
	}
	env := make(map[string]api.Secret, len(names))
	for _, name := range names {
		value := a.ExposedValues[name]
		next.Secrets[name] = string(value)
		env[name] = value
	}
	return next, env, names, nil
}

func (state *memberState) memberChanges(a MemberAddArgs) bool {
	if a.Kind != "" && !slices.Contains(state.kinds, a.Kind) {
		return true
	}
	for name := range a.ExposedValues {
		if _, exists := state.secrets[name]; !exists {
			return true
		}
	}
	return false
}
