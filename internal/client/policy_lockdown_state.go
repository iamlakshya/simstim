package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
)

const policyLockdownFileV1 = "lockdown.json"

type policyLockdownBindingV1 struct {
	EntryHash string `json:"entry_hash"`
	HostID    string `json:"host_id,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
}
type policyLockdownPendingV1 struct {
	On      bool                    `json:"on"`
	Binding policyLockdownBindingV1 `json:"binding"`
}
type localLockdownStateV1 struct {
	Version int                                `json:"v"`
	Hosts   map[string]policyLockdownBindingV1 `json:"hosts"`
}

func validLockdownBindingV1(binding policyLockdownBindingV1) bool {
	return api.ValidPolicyHashV1(binding.EntryHash) && (binding.HostID == "" || ValidHostID(binding.HostID))
}

func validateLockdownBindingRawV1(raw json.RawMessage) error {
	fields, err := api.JSONObjectFields(raw, api.ExactJSONFieldNames(policyLockdownBindingV1{})...)
	var binding policyLockdownBindingV1
	if err != nil || !api.ExactJSONFields(raw, binding) || fields["entry_hash"] == nil || json.Unmarshal(raw, &binding) != nil || !validLockdownBindingV1(binding) {
		return badGrantStateError(policyLockdownFileV1)
	}
	if value, exists := fields["host_id"]; exists && (bytes.Equal(bytes.TrimSpace(value), []byte("null")) || !ValidHostID(binding.HostID)) {
		return badGrantStateError(policyLockdownFileV1)
	}
	if value, exists := fields["pending"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("true")) && !bytes.Equal(bytes.TrimSpace(value), []byte("false")) {
		return badGrantStateError(policyLockdownFileV1)
	}
	return nil
}

func validateLockdownPendingRawV1(raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	rows, err := api.JSONObjectFields(raw)
	if err != nil {
		return badGrantStateError(policyPendingFile)
	}
	for host, row := range rows {
		fields, err := api.JSONObjectFields(row, "on", "binding")
		if err != nil || len(fields) != 2 || !ValidHostName(host) || !bytes.Equal(bytes.TrimSpace(fields["on"]), []byte("true")) && !bytes.Equal(bytes.TrimSpace(fields["on"]), []byte("false")) {
			return badGrantStateError(policyPendingFile)
		}
		if err := validateLockdownBindingRawV1(fields["binding"]); err != nil {
			return err
		}
	}
	return nil
}

func readLocalLockdownViewV1(view stateFileView) (localLockdownStateV1, error) {
	state := localLockdownStateV1{Version: 1, Hosts: map[string]policyLockdownBindingV1{}}
	if !view.Exists {
		return state, nil
	}
	if view.Size < 1 || view.Size > MaxConfigSize {
		return state, badGrantStateError(policyLockdownFileV1)
	}
	raw, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
	if err != nil {
		return state, err
	}
	if !api.ExactJSONFields(raw, state) || json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Hosts == nil || len(state.Hosts) > 1024 {
		return state, badGrantStateError(policyLockdownFileV1)
	}
	fields, err := api.JSONObjectFields(raw, "v", "hosts")
	if err != nil {
		return state, err
	}
	rows, err := api.JSONObjectFields(fields["hosts"])
	if err != nil {
		return state, err
	}
	for _, row := range rows {
		if err := validateLockdownBindingRawV1(row); err != nil {
			return state, err
		}
	}
	for host, binding := range state.Hosts {
		if !ValidHostName(host) || !validLockdownBindingV1(binding) {
			return state, badGrantStateError(policyLockdownFileV1)
		}
	}
	return state, nil
}

func readLocalLockdownV1(ctx context.Context) (localLockdownStateV1, error) {
	var state localLockdownStateV1
	err := withStateFileFD(ctx, nil, policyLockdownFileV1, false, func(view stateFileView) (stateFileReplace, error) {
		var err error
		state, err = readLocalLockdownViewV1(view)
		return nil, err
	})
	return state, err
}

func updateLocalLockdownV1(ctx context.Context, add map[string]policyLockdownBindingV1, remove []string) error {
	if err := checkPolicyChange(ctx); err != nil {
		return err
	}
	return withStateTransaction(ctx, nil, policyLockdownFileV1, true, false, func(tx *stateFileTxn) error {
		state, err := readLocalLockdownViewV1(tx.view())
		if err != nil {
			return err
		}
		for _, host := range remove {
			delete(state.Hosts, host)
		}
		for host, binding := range add {
			if !ValidHostName(host) || !validLockdownBindingV1(binding) {
				return badGrantStateError(policyLockdownFileV1)
			}
			state.Hosts[host] = binding
		}
		if len(state.Hosts) > 1024 {
			return badGrantStateError(policyLockdownFileV1)
		}
		if len(state.Hosts) == 0 {
			if !tx.view().Exists {
				return nil
			}
			if err := tx.removalOwnership(); err != nil {
				return err
			}
			tx.revokeReader()
			return tx.removeExact()
		}
		return tx.replace(func(ctx context.Context, w io.Writer) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return json.NewEncoder(w).Encode(state)
		})
	})
}

func updateLockdownPendingV1(ctx context.Context, add map[string]policyLockdownPendingV1, remove []string) error {
	if err := checkPolicyChange(ctx); err != nil {
		return err
	}
	return withStateFileFD(ctx, nil, policyPendingFile, true, func(view stateFileView) (stateFileReplace, error) {
		state, err := readPolicyPendingView(view)
		if err != nil {
			return nil, err
		}
		if state.Lockdowns == nil {
			state.Lockdowns = map[string]policyLockdownPendingV1{}
		}
		for _, host := range remove {
			delete(state.Lockdowns, host)
		}
		for host, pending := range add {
			if !ValidHostName(host) || !validLockdownBindingV1(pending.Binding) {
				return nil, badGrantStateError(policyPendingFile)
			}
			state.Lockdowns[host] = pending
		}
		if len(state.Lockdowns) > 1024 {
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

func lockdownBindingForHostV1(host Host) (policyLockdownBindingV1, error) {
	hash, err := policyLockdownEntryHashV1(host)
	if err != nil {
		return policyLockdownBindingV1{}, err
	}
	binding := policyLockdownBindingV1{EntryHash: hash, Pending: true}
	tiers, err := readTiersFile()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return binding, err
	}
	if err == nil {
		if entry, exists := tiers.Entry(host.Name); exists {
			binding.HostID = entry.HostID
		}
	}
	return binding, nil
}

func checkLockdownBindingV1(host Host, hostID string, binding policyLockdownBindingV1) error {
	hash, err := policyLockdownEntryHashV1(host)
	if err != nil {
		return err
	}
	if hash != binding.EntryHash || binding.HostID != "" && binding.HostID != hostID {
		return policyLockdownSourceErrorV1()
	}
	return nil
}

// replayLockdownContactV1 consumes only recorded pending intent on this exact
// accepted host. An off record wins over an interrupted local-on cleanup.
func replayLockdownContactV1(ctx context.Context, c *Conn, pending policyPendingState) error {
	intent, found := pending.Lockdowns[c.Host.Name]
	if !found {
		active, err := readLocalLockdownV1(ctx)
		if err != nil {
			return err
		}
		binding, exists := active.Hosts[c.Host.Name]
		if !exists || !binding.Pending {
			return nil
		}
		intent, found = policyLockdownPendingV1{On: true, Binding: binding}, true
	}
	if !found {
		return nil
	}
	if err := checkLockdownBindingV1(c.Host, c.HostID(), intent.Binding); err != nil {
		return err
	}
	if !intent.On {
		if err := updateLocalLockdownV1(ctx, nil, []string{c.Host.Name}); err != nil {
			return err
		}
	}
	literal := ""
	if !intent.On {
		literal = "lockdown"
	} // The same recorded fixed action, never a credential answer.
	request, err := NewPolicyLockdownRequestV1(c.Host, intent.On, literal)
	if err != nil {
		return err
	}
	defer request.Close()
	if _, err := request.Apply(request.Context(ctx), c); err != nil {
		return err
	}
	if intent.On {
		binding := intent.Binding
		binding.HostID, binding.Pending = c.HostID(), false
		if err := updateLocalLockdownV1(ctx, map[string]policyLockdownBindingV1{c.Host.Name: binding}, nil); err != nil {
			return err
		}
	}
	return updateLockdownPendingV1(ctx, nil, []string{c.Host.Name})
}
