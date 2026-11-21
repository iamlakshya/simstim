package client

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/nettarget"
)

// Only the actual private completed export and original selected local layer
// enter this writer. SESSION persistence belongs to the existing server writer.
func persistResolvedNetworkHoldV1(ctx context.Context, operation *networkHoldOperation) (out FilePolicyResult, failure error) {
	if operation == nil || operation.config == nil || operation.request != grantRequestOf(ctx) ||
		ctx.Value(networkHoldFilePublicationKey{}) != operation {
		return out, networkHoldConfigErrorV1()
	}
	if err := operation.checkLocal(ctx); err != nil {
		return out, err
	}
	operation.mu.Lock()
	selected := operation.selected
	resolution := operation.resolution
	valid := operation.busy && !operation.closed && !operation.published && resolution != nil &&
		(selected.Kind == api.LayerRepo && operation.args.Scope == api.NetworkHoldRepo || selected.Kind == api.LayerProfile && operation.args.Scope == api.NetworkHoldProfile)
	operation.mu.Unlock()
	if !valid || selected.Name == "" {
		return out, networkHoldConfigErrorV1()
	}
	operation.mu.Lock()
	descriptor := operation.descriptor
	operation.mu.Unlock()
	if descriptor == nil {
		return out, networkHoldSourceError()
	}
	// This only narrows the caller's existing budget to the ORIGINAL expiry.
	// It covers lock waits and every pre-rename native snapshot without renewal.
	bounded, cancel := context.WithDeadline(ctx, descriptor.Expires)
	defer cancel()
	owned, release, err := operation.conn.beginGrantSource(bounded, "publish held network policy", true)
	if err != nil {
		return out, err
	}
	defer release()
	rule, err := resolvedNetworkHoldRuleV1(operation, selected, *resolution)
	if err != nil {
		return out, err
	}
	settings, err := operation.settings.Settings(owned)
	if err != nil {
		return out, err
	}
	operation.mu.Lock()
	tier, maximum := operation.tier, operation.maximum
	operation.mu.Unlock()
	outbound, err := policyOutboundForRule(rule)
	if err != nil {
		return out, err
	}
	if err := allowRequestGrant(owned, operation.conn.Host, operation.conn.HostID(), tier, outbound, settings); err != nil {
		return out, err
	}
	if err := operation.config.authorizeFile(owned, tier, maximum); err != nil {
		return out, err
	}
	// Permission questions finish before any local folder, lock or temp is made.
	if err := operation.beforeFilePublicationV1(owned); err != nil {
		return out, err
	}
	config := operation.config
	tx, err := config.beginTransaction(owned)
	if err != nil {
		return out, err
	}
	defer config.endTransaction(tx)
	unlock, err := lockStateProcess(owned, stateProcessMutex([]string{"config", config.path}, tx.name))
	if err != nil {
		return out, err
	}
	defer unlock()
	tx.lock, err = acquireStateFileLock(owned, tx.root, tx.name, true)
	if err != nil {
		return out, err
	}
	defer func() { failure = errors.Join(failure, tx.lock.closeOwned(owned)) }()
	if err := config.openTransactionSource(tx); err != nil {
		return out, err
	}
	failure = tx.apply(func(tx *stateFileTxn) error {
		view := tx.view()
		if view.Size < 0 || view.Size > MaxConfigSize {
			return networkHoldConfigErrorV1()
		}
		raw, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
		if err != nil {
			return err
		}
		encoded, result, changed, err := encodeResolvedNetworkHoldPolicyV1(operation, selected, *resolution, raw)
		if err != nil {
			return err
		}
		out = result
		if !changed {
			return nil
		}
		return tx.replace(func(writeCtx context.Context, writer io.Writer) error {
			if err := writeCtx.Err(); err != nil {
				return err
			}
			_, err := writer.Write(encoded)
			return err
		})
	}, false)
	var callback *stateCallbackError
	if errors.As(failure, &callback) && failure == callback {
		failure = callback.err
	}
	return out, failure
}

func encodeResolvedNetworkHoldPolicyV1(operation *networkHoldOperation, selected FileLayer, resolution api.NetworkHoldResolution, original []byte) ([]byte, FilePolicyResult, bool, error) {
	var out FilePolicyResult
	rule, err := resolvedNetworkHoldRuleV1(operation, selected, resolution)
	if err != nil {
		return nil, out, false, err
	}
	document := map[string]any{}
	if len(original) != 0 {
		if _, err := toml.Decode(string(original), &document); err != nil {
			return nil, out, false, tomlError(settingsPath(), string(original), err)
		}
	}
	section, err := policyFileSection(document, selected)
	if err != nil {
		return nil, out, false, err
	}
	changed, _, err := editFileRules(section, rule, "")
	if err != nil {
		return nil, out, false, err
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(document); err != nil || encoded.Len() > MaxConfigSize {
		return nil, out, false, networkHoldConfigErrorV1()
	}
	var next Settings
	if _, err := decodeConfig(encoded.String(), &next); err != nil {
		return nil, out, false, tomlError(settingsPath(), encoded.String(), err)
	}
	origin, profile := "", ""
	if selected.Kind == api.LayerRepo {
		origin = selected.Name
	} else {
		profile = selected.Name
	}
	layers, err := ResolvePolicyLayers(withDefaults(next), operation.conn.Host, origin, profile, api.LayerValues{})
	if err != nil {
		return nil, out, false, err
	}
	out.Policy, _, err = egress.MergeLayers(layers)
	if err != nil {
		return nil, out, false, policyError(err)
	}
	out.Rule, out.Applies = rule, "next_start"
	return encoded.Bytes(), out, changed, nil
}

func resolvedNetworkHoldRuleV1(operation *networkHoldOperation, selected FileLayer, resolution api.NetworkHoldResolution) (api.Rule, error) {
	target, err := nettarget.Parse(resolution.Target)
	if err != nil || target.String() != resolution.Target || resolution.Class != api.NetworkHoldPublicTarget || target.Wildcard ||
		target.Prefix.IsValid() && (!heldPublicLiteralV1(target) || target.Prefix.Bits() != target.Prefix.Addr().BitLen()) {
		return api.Rule{}, networkHoldConfigErrorV1()
	}
	operation.mu.Lock()
	maximum := operation.maximum
	operation.mu.Unlock()
	rule := api.Rule{Layer: selected.Kind, Name: selected.Name, Door: api.DoorNet, Allow: true, Target: resolution.Target,
		PublicOnly: target.Prefix.IsValid() || maximum == api.NetworkHoldPublicOnly}
	rule.ID = api.RuleID(rule)
	return rule, nil
}
