package client

import (
	"context"
	"encoding/json"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

const policyPendingFile = "policy-pending.json"

type policyPendingState struct {
	Version   int                                `json:"v"`
	Hosts     []string                           `json:"hosts"`
	Lockdowns map[string]policyLockdownPendingV1 `json:"lockdowns,omitempty"`
}

func readPolicyPendingView(view stateFileView) (policyPendingState, error) {
	state := policyPendingState{Version: 1, Hosts: []string{}}
	if !view.Exists {
		return state, nil
	}
	if view.Size < 1 || view.Size > MaxConfigSize {
		return state, badGrantStateError(policyPendingFile)
	}
	raw, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
	if err != nil || !api.ExactJSONFields(raw, policyPendingState{}) {
		return state, badGrantStateError(policyPendingFile)
	}
	if json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Hosts == nil || len(state.Hosts) > 1024 {
		return state, badGrantStateError(policyPendingFile)
	}
	if len(state.Lockdowns) > 1024 {
		return state, badGrantStateError(policyPendingFile)
	}
	fields, err := api.JSONObjectFields(raw, api.ExactJSONFieldNames(state)...)
	if err != nil {
		return state, badGrantStateError(policyPendingFile)
	}
	if pending, exists := fields["lockdowns"]; exists {
		if err := validateLockdownPendingRawV1(pending); err != nil {
			return state, badGrantStateError(policyPendingFile)
		}
	}
	seen := map[string]bool{}
	for _, host := range state.Hosts {
		if !ValidHostName(host) || seen[host] {
			return state, badGrantStateError(policyPendingFile)
		}
		seen[host] = true
	}
	return state, nil
}

func readPolicyPending(ctx context.Context) (policyPendingState, error) {
	var state policyPendingState
	err := withStateFileFD(ctx, nil, policyPendingFile, false, func(view stateFileView) (stateFileReplace, error) {
		var err error
		state, err = readPolicyPendingView(view)
		return nil, err
	})
	return state, err
}

func updatePolicyPending(ctx context.Context, reached, missed []string) error {
	return withStateFileFD(ctx, nil, policyPendingFile, true, func(view stateFileView) (stateFileReplace, error) {
		state, err := readPolicyPendingView(view)
		if err != nil {
			return nil, err
		}
		names := map[string]bool{}
		for _, name := range state.Hosts {
			names[name] = true
		}
		for _, name := range reached {
			if !ValidHostName(name) {
				return nil, badGrantStateError(policyPendingFile)
			}
			delete(names, name)
		}
		for _, name := range missed {
			if !ValidHostName(name) {
				return nil, badGrantStateError(policyPendingFile)
			}
			names[name] = true
		}
		state.Hosts = make([]string, 0, len(names))
		for name := range names {
			state.Hosts = append(state.Hosts, name)
		}
		slices.Sort(state.Hosts)
		if len(state.Hosts) > 1024 {
			return nil, badGrantStateError(policyPendingFile)
		}
		return func(ctx context.Context, w io.Writer) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return json.NewEncoder(w).Encode(state)
		}, nil
	})
}
