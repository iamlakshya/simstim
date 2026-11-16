package egress

import (
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const decoyRequestBodyBytes = 1 << 20

func decoysEnabled(policy api.Policy) bool {
	return policy.Sandbox.Decoys == nil || *policy.Sandbox.Decoys
}

func prepareDecoyInput(input []api.DecoyTripwire, policy api.Policy) ([]api.DecoyTripwire, []compiledRule, error) {
	// A disabled generation is ignored before even reading the public data.
	if !decoysEnabled(policy) || len(input) == 0 {
		return nil, nil, nil
	}
	definitions, err := proto.DecoyDefinitions()
	if err != nil {
		return nil, nil, err
	}
	if len(input) != len(definitions) {
		return nil, nil, memberError("bad_request", "the decoy tripwire generation is incomplete", "Start the sandbox with matching helper files.")
	}
	seen := make(map[string]bool)
	owned := api.CloneDecoyTripwires(input)
	var classes []compiledRule
	for index, tripwire := range owned {
		position := slices.IndexFunc(definitions, func(definition proto.DecoyDefinition) bool { return definition.Name == tripwire.Name })
		if position < 0 || seen[tripwire.Name] || !slices.Equal(tripwire.Hosts, definitions[position].Hosts) {
			return nil, nil, memberError("bad_request", "the decoy tripwire generation differs from its canonical data", "Start the sandbox with matching helper files.")
		}
		seen[tripwire.Name] = true
		values := make([]string, len(tripwire.Values))
		for i, value := range tripwire.Values {
			values[i] = string(value)
		}
		if _, err := proto.RenderDecoy(tripwire.Name, values); err != nil {
			return nil, nil, err
		}
		for _, host := range definitions[position].Hosts {
			class, err := compileServiceHost(host)
			if err != nil {
				return nil, nil, memberError("bad_request", "a decoy host class cannot be inspected", "Use matching reviewed decoy data.")
			}
			classes = append(classes, class)
		}
		owned[index].Hosts = slices.Clone(definitions[position].Hosts)
	}
	return owned, classes, nil
}

// Every raw byte remains represented in these exact base64 windows. Only
// bits outside the value vary: at most four leading and four trailing bits.
// This covers all three byte alignments within a standard base64 payload,
// including values embedded in a larger encoded buffer, without decoding or
// retaining that buffer and without adding another matcher.
func decoyValueForms(value api.Secret) []api.Secret {
	if value == "" {
		return nil
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	seen := map[api.Secret]bool{value: true}
	for alignment := 0; alignment < 3; alignment++ {
		input := make([]byte, alignment+len(value))
		copy(input[alignment:], string(value))
		encoded := base64.StdEncoding.EncodeToString(input)
		clear(input)
		start, end := alignment*8/6, ((alignment+len(value))*8+5)/6
		window := []byte(encoded[start:end])
		leading := alignment * 8 % 6
		trailing := (6 - (alignment+len(value))*8%6) % 6
		first := strings.IndexByte(alphabet, window[0])
		last := strings.IndexByte(alphabet, window[len(window)-1])
		for before := 0; before < 1<<leading; before++ {
			for after := 0; after < 1<<trailing; after++ {
				window[0] = alphabet[first&((1<<(6-leading))-1)|before<<(6-leading)]
				window[len(window)-1] = alphabet[last&(^((1<<trailing)-1)&63)|after]
				seen[api.Secret(window)] = true
			}
		}
		clear(window)
	}
	forms := make([]api.Secret, 0, len(seen))
	for form := range seen {
		forms = append(forms, form)
	}
	sort.Slice(forms, func(i, j int) bool { return forms[i] < forms[j] })
	return forms
}

func (s *requestSecretSnapshot) withDecoyTripwires(input []api.DecoyTripwire) (*requestSecretSnapshot, error) {
	if len(input) == 0 {
		return s, nil
	}
	labels := make(map[api.Secret]string)
	for _, tripwire := range input {
		for _, value := range tripwire.Values {
			if value == "" {
				return nil, memberError("bad_request", "a decoy tripwire is empty", "Start a fresh sandbox with matching helper files.")
			}
			for _, form := range decoyValueForms(value) {
				if old := labels[form]; old != "" && old != tripwire.Name {
					return nil, memberError("bad_request", "decoy tripwire identities collide", "Start the sandbox again to make a fresh decoy generation.")
				}
				labels[form] = tripwire.Name
			}
		}
	}
	for _, value := range s.tripwires {
		if labels[value] != "" {
			return nil, memberError("bad_request", "a decoy collides with a current public stand-in", "Start a fresh sandbox to make a distinct decoy generation.")
		}
	}
	values := make([]api.Secret, 0, len(labels))
	for value := range labels {
		values = append(values, value)
	}
	output, err := s.withTripwireSentinels(values)
	if err != nil {
		return nil, err
	}
	output.tripwireLabels = make([]string, len(output.tripwires))
	body := &requestSecretSnapshot{tripwires: slices.Clone(values)}
	sort.Slice(body.tripwires, func(i, j int) bool { return body.tripwires[i] < body.tripwires[j] })
	body.tripwireLabels = make([]string, len(body.tripwires))
	for index, value := range output.tripwires {
		output.tripwireLabels[index] = labels[value]
	}
	for index, value := range body.tripwires {
		body.tripwireLabels[index] = labels[value]
	}
	output.decoyView = body
	return output, nil
}

func decoyScrubValues(input map[string]string, tripwires []api.DecoyTripwire) map[string]string {
	if len(tripwires) == 0 {
		return input
	}
	output := maps.Clone(input)
	if output == nil {
		output = make(map[string]string)
	}
	ordinal := 0
	for _, tripwire := range tripwires {
		for _, value := range tripwire.Values {
			for _, form := range decoyValueForms(value) {
				output[fmt.Sprintf("decoy %s/%d", tripwire.Name, ordinal)] = string(form)
				ordinal++
			}
		}
	}
	return output
}

func (s *requestSecretSnapshot) tripwireRefusal(index int) *requestSecretFailure {
	failure := requestSecretRefusal(403, "secret_misuse")
	if index >= 0 && index < len(s.tripwireLabels) {
		failure.decoy = s.tripwireLabels[index]
	}
	return failure
}

func (f *requestSecretFailure) auditRule() string {
	if f.decoy != "" {
		return "decoy"
	}
	return f.detail.Code
}

type decoyStreamUse struct{ name string }

func (e *decoyStreamUse) Unwrap() error { return errRequestSecretStreamMisuse }
func (e *decoyStreamUse) Error() string {
	return "a decoy credential was used. Try: review this request before continuing"
}
func (e *decoyStreamUse) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, "<decoy use>") }

func (state *memberState) decoyHost(host string, port int) bool {
	if state == nil || port != 443 || len(state.decoys) == 0 {
		return false
	}
	canonical, err := NormaliseHost(host)
	if err != nil {
		return false
	}
	for _, class := range state.decoyClasses {
		if matchesServiceHost(class, canonical) {
			return true
		}
	}
	return false
}

func (s *Server) decoyHost(host string, port int) bool {
	return s.currentMembers().decoyHost(host, port)
}

func (state *memberState) withAppliedDecoyPolicy(proposed api.Policy) api.Policy {
	proposed.Sandbox.Decoys = nil
	if applied := state.metadata.Policy.Sandbox.Decoys; applied != nil {
		value := *applied
		proposed.Sandbox.Decoys = &value
	}
	return proposed
}

func decoyAuditDestination(host string, failure *requestSecretFailure) string {
	if failure != nil && failure.decoy != "" {
		return "[decoy destination]"
	}
	return host
}
