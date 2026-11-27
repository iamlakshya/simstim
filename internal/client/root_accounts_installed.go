package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

type rootAccountSlotObservation struct {
	Number      int    `json:"slot"`
	Free        bool   `json:"free"`
	Session     string `json:"session,omitempty"`
	UID         int    `json:"uid,omitempty"`
	SlotUID     int    `json:"slot_uid,omitempty"`
	Quarantined bool   `json:"quarantined,omitempty"`
}
type rootAccountInstalledObservation struct {
	OK    bool                         `json:"ok"`
	Error string                       `json:"error,omitempty"`
	Code  string                       `json:"code,omitempty"`
	Hint  string                       `json:"hint,omitempty"`
	Slots []rootAccountSlotObservation `json:"slots,omitempty"`
}

// verifyInstalled is reachable only after this same original RootRun's actual
// process and reviewed apply have joined. It observes the root-owned exact
// release executable and the fixed launcher's actual native slot inventory.
func (p *rootAccountPreparation) verifyInstalled(ctx context.Context, c *Conn, probe api.CapProbe) error {
	if p == nil || c != p.conn || grantRequestOf(ctx) != p.request || probe.User != p.user || probe.UID != p.uid || probe.Arch != p.arch {
		return grantPurposeError()
	}
	r := p.request
	r.mu.Lock()
	intent, execution := r.root, r.rootExecution
	r.mu.Unlock()
	if intent == nil || execution == nil || intent.conn != c || intent.script.accounts != p || intent.mode != RootRun || execution.intent != intent {
		return grantPurposeError()
	}
	execution.mu.Lock()
	applied := execution.applyDone && execution.done && !execution.closed
	execution.mu.Unlock()
	if !applied {
		return grantOperationError()
	}
	process, output, err := p.startStage(ctx, rootAccountInstalled)
	if err != nil {
		return err
	}
	if err := waitRootStepProcess(ctx, process); err != nil {
		return errors.Join(rootStepError("the installed launcher did not finish its actual account check"), err)
	}
	var actual rootAccountInstalledObservation
	if !api.ExactJSONFields(output.data, actual) {
		return rootStepError("the installed launcher returned an unknown native inventory shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(output.data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&actual) != nil || decoder.Decode(new(any)) != io.EOF || !actual.OK || len(actual.Slots) != 16 {
		return rootStepError("the installed launcher did not confirm its 16 native slot accounts")
	}
	seen := map[int]bool{}
	for i, slot := range actual.Slots {
		if slot.Number != i || slot.SlotUID <= 0 || seen[slot.SlotUID] {
			return rootStepError("the installed launcher has an incomplete or aliased native slot inventory")
		}
		seen[slot.SlotUID] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.staged || p.process != nil || p.starting {
		return grantOperationError()
	}
	p.installed = true
	return nil
}

func (p *rootAccountPreparation) installObserved(probe api.CapProbe) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.installed && !p.closed && p.staged && p.user == probe.User && p.uid == probe.UID && p.arch == probe.Arch
}
