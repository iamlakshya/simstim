package client

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sort"
)

// The typed encoder, selected-generation forget, legacy initialisation and
// policy document all end at this SAME writer. Only the first canonical marker
// changes; unknown TOML bytes remain owned by their original document.
func writeHostsDocumentGeneration(document []byte, generation string) error {
	if !ValidHostID(generation) {
		return hostStateRemovalError("the selected host membership generation is invalid")
	}
	body := document
	if bytes.HasPrefix(body, []byte("# membership-generation")) {
		if _, err := readMembershipGeneration(context.Background(), bytes.NewReader(body)); err != nil {
			return err
		}
		_, body, _ = bytes.Cut(body, []byte{'\n'})
	}
	var encoded bytes.Buffer
	encoded.Grow(len(membershipGenerationPrefix) + len(generation) + 1 + len(body))
	encoded.WriteString(membershipGenerationPrefix + generation + "\n")
	encoded.Write(body)
	return writeAtomicFile(hostsPath(), ".hosts.toml-*", encoded.Bytes())
}

// writeHostPolicyDocumentContext changes one actual saved host's known shape
// while retaining the original document. Call outside membership callbacks.
// It takes or reuses actual OP/exclusive ownership; it never mints a Source.
func writeHostPolicyDocumentContext(ctx context.Context, expected Host, next []Host, encoded []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHostName(expected.Name) {
		return badHostNameError(expected.Name)
	}
	expected = cloneObservationHost(expected)
	frozen := make([]Host, len(next))
	for i, host := range next {
		if err := ctx.Err(); err != nil {
			return err
		}
		frozen[i] = cloneObservationHost(host)
	}
	document := bytes.Clone(encoded)
	var parsed hostsFile
	if _, err := decodeConfig(string(document), &parsed); err != nil {
		return tomlError(hostsPath(), string(document), err)
	}
	if !sameHostDocumentRows(parsed.Hosts, frozen) {
		return hostStateRemovalError("the policy document differs from its declared saved hosts")
	}
	selected, err := singleHostDocumentEntry(frozen, expected.Name)
	if err != nil {
		return err
	}
	ctx, unlock, err := LockHost(ctx, expected.Name, "policy")
	if err != nil {
		return err
	}
	defer unlock()
	if observed, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); observed != nil && observed.role != hostStateSaved && observed.role != hostStateRead {
		return hostStateRemovalError("a policy edit cannot borrow a registration or creation publication")
	}
	ctx, endMutation, err := holdHostMutationContext(ctx, expected.Name)
	if err != nil {
		return err
	}
	defer endMutation()
	return updateHostsFileContext(ctx, func(locked context.Context, hosts []Host) ([]Host, hostFileHooks, error) {
		return prepareHostPolicyDocumentChange(locked, expected, selected, frozen, document, hosts)
	})
}

func prepareHostPolicyDocumentChange(locked context.Context, expected Host, selected *Host, frozen []Host, document []byte, hosts []Host) ([]Host, hostFileHooks, error) {
	if err := checkTierMutationSource(locked, expected); err != nil {
		return nil, hostFileHooks{}, err
	}
	if _, err := singleHostDocumentEntry(hosts, expected.Name); err != nil {
		return nil, hostFileHooks{}, err
	}
	change := HostChange{Kind: "policy", Was: &expected, Now: selected}
	out, err := ApplyHostChange(hosts, change)
	if err != nil {
		return nil, hostFileHooks{}, err
	}
	if !sameHostDocumentRows(out, frozen) {
		return nil, hostFileHooks{}, hostStateRemovalError("a policy edit changed another saved host")
	}
	current, err := currentMembershipGeneration(locked)
	if err != nil {
		return nil, hostFileHooks{}, err
	}
	original, err := readMembershipGeneration(locked, bytes.NewReader(document))
	if err != nil || original != current {
		return nil, hostFileHooks{}, hostStateRemovalError("the edited policy document belongs to an older saved-host Source")
	}
	generation, err := newMembershipGeneration()
	if err != nil {
		return nil, hostFileHooks{}, fmt.Errorf("make policy membership generation: %w", err)
	}
	for generation == current {
		if err := locked.Err(); err != nil {
			return nil, hostFileHooks{}, err
		}
		generation, err = newMembershipGeneration()
		if err != nil {
			return nil, hostFileHooks{}, err
		}
	}
	out, hooks, err := changeHostFiles(locked, change, hosts, false, nil)
	if err != nil {
		return nil, hooks, err
	}
	hooks.document, hooks.membershipGeneration, hooks.membershipSourceGeneration = document, generation, current
	prior := hooks.beforeSave
	hooks.beforeSave = func() error {
		if err := checkTierMutationSource(locked, expected); err != nil {
			return err
		}
		if prior != nil {
			return prior()
		}
		return locked.Err()
	}
	return out, hooks, nil
}

func singleHostDocumentEntry(hosts []Host, name string) (*Host, error) {
	var selected *Host
	for i := range hosts {
		if hosts[i].Name == name {
			if selected != nil {
				return nil, hostStateRemovalError("a policy edit cannot choose a repeated saved host name")
			}
			selected = &hosts[i]
		}
	}
	if selected == nil {
		return nil, hostStateRemovalError("the edited policy document removed its selected host")
	}
	return selected, nil
}

func sameHostDocumentRows(a, b []Host) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = slices.Clone(a), slices.Clone(b)
	sort.SliceStable(a, func(i, j int) bool { return a[i].Name < a[j].Name })
	sort.SliceStable(b, func(i, j int) bool { return b[i].Name < b[j].Name })
	return slices.EqualFunc(a, b, SameEntry)
}

// hostPolicyDocumentWithSourceContext carries only the exact original marker
// when the canonical TOML editor emits a body without comments. It supplies no
// Source authority; the actual OP and final writer still compare that Source.
func hostPolicyDocumentWithSourceContext(ctx context.Context, original, edited []byte) ([]byte, error) {
	generation, err := readMembershipGeneration(ctx, bytes.NewReader(original))
	if err != nil {
		return nil, err
	}
	body := edited
	if bytes.HasPrefix(body, []byte("# membership-generation")) {
		existing, err := readMembershipGeneration(ctx, bytes.NewReader(body))
		if err != nil || existing != generation {
			return nil, hostStateRemovalError("the edited policy body carries another membership Source")
		}
		_, body, _ = bytes.Cut(body, []byte{'\n'})
	}
	line, _, _ := bytes.Cut(original, []byte{'\n'})
	result := make([]byte, 0, len(line)+1+len(body))
	result = append(result, line...)
	result = append(result, '\n')
	result = append(result, body...)
	return result, ctx.Err()
}
