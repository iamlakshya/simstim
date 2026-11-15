package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
)

// FileLayer identifies a laptop policy section. A repository name is its
// canonical origin; the caller resolves a local folder before editing.
type FileLayer struct {
	Kind api.LayerKind
	Name string
}

// FilePolicyResult describes a persistent edit or its dry run.
type FilePolicyResult struct {
	Rule    api.Rule   `json:"rule,omitempty"`
	Removed int        `json:"removed,omitempty"`
	Policy  api.Policy `json:"policy"`
	Applies string     `json:"applies"`
}

// ChangeFilePolicy adds or removes a rule under a bounded cross-process lock.
// Dry runs create no directories, locks or temporary files. Unknown config
// tables survive the round trip, and file rules never acquire stored IDs.
func ChangeFilePolicy(ctx context.Context, layer FileLayer, rule api.Rule, remove string, dryRun bool) (FilePolicyResult, error) {
	ctx, _ = ensureGrantRequest(ctx)
	var out FilePolicyResult
	if _, err := ReadPolicySettings(); err != nil {
		return out, err
	}
	path := settingsPath()
	switch layer.Kind {
	case api.LayerAll:
	case api.LayerHost:
		path = hostsPath()
	case api.LayerRepo, api.LayerProfile:
		if layer.Name == "" {
			return out, missingFileLayer()
		}
	default:
		return out, missingFileLayer()
	}
	if rule.Door != "" && rule.Door != api.DoorNet {
		return out, &UserError{Msg: "file policy edits currently use the net door", Hint: "Pass net, or leave the door out.", Code: "bad_door", Kind: KindUsage}
	}
	if remove != "" {
		if target, err := egress.ValidateRuleTarget(remove); err == nil {
			remove = target
		}
	}
	if rule.Once || !rule.Until.IsZero() || rule.Mode != "" || len(rule.Tools) != 0 {
		return out, &UserError{Msg: "file layers cannot hold once, duration or tool grants", Hint: "Use --in <host>/<session> for a temporary grant.", Code: "layer_key", Kind: KindUsage}
	}
	if !dryRun {
		if _, err := ChangeFilePolicy(ctx, layer, rule, remove, true); err != nil {
			return out, err
		}
		if err := ctx.Err(); err != nil {
			return out, stoppedBefore("write the policy file", err)
		}
		if layer.Kind == api.LayerHost {
			var unlockHost func()
			var lockErr error
			ctx, unlockHost, lockErr = LockHost(ctx, layer.Name, "policy")
			if lockErr != nil {
				return out, lockErr
			}
			defer unlockHost()
			if _, err := ChangeFilePolicy(ctx, layer, rule, remove, true); err != nil {
				return out, err
			}
		}
		if layer.Kind != api.LayerHost {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return out, fmt.Errorf("make the config folder: %w", err)
			}
			unlock, err := lockPathContext(ctx, path+".lock", 2*time.Second, true)
			if err != nil {
				return out, fmt.Errorf("lock the policy file: %w", err)
			}
			defer unlock()
		}
	}
	// Reread after the lock. Another client may have edited either file.
	settings, err := ReadPolicySettings()
	if err != nil {
		return out, err
	}
	hosts, err := LoadHosts()
	if err != nil {
		return out, err
	}
	b, err := ReadConfigFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, fmt.Errorf("read the policy file: %w", err)
	}
	document := map[string]any{}
	if len(b) > 0 {
		if _, err := toml.Decode(string(b), &document); err != nil {
			return out, tomlError(path, string(b), err)
		}
	}
	section, err := policyFileSection(document, layer)
	if err != nil {
		return out, err
	}
	var h Host
	if layer.Kind == api.LayerHost {
		at := slices.IndexFunc(hosts, func(h Host) bool { return h.Name == layer.Name })
		if at < 0 {
			return out, &UserError{Msg: "no host named " + CleanLine(layer.Name), Hint: brand.Name + " host ls", Code: "host_not_found", Kind: KindNotFound}
		}
		h = hosts[at]
	}
	grantHost := h
	if layer.Kind == api.LayerHost {
		if err := bindRequestLocalSource(ctx, grantHost); err != nil {
			return out, err
		}
	}
	methods, methodErr := api.CanonicalHTTPMethods(rule.Methods)
	if methodErr != nil {
		return out, &UserError{Msg: methodErr.Error(), Hint: "Use exact HTTP method tokens.", Code: "bad_method", Kind: KindUsage}
	}
	rule.Methods = methods
	rule.Layer, rule.Name, rule.Door = layer.Kind, layer.Name, api.DoorNet
	if remove == "" {
		target, err := egress.ValidateRuleTarget(rule.Target)
		if err != nil {
			return out, policyError(err)
		}
		rule.Target = target
		rule.ID = api.RuleID(rule)
	}
	changed, removed, err := editFileRules(section, rule, remove)
	if err != nil {
		return out, err
	}
	out.Rule, out.Removed, out.Applies = rule, removed, "next_start"
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(document); err != nil {
		return out, fmt.Errorf("encode the policy file: %w", err)
	}
	var nextHosts []Host
	var check any = &settings
	if layer.Kind == api.LayerHost {
		check = &hostsFile{}
	}
	if _, err := decodeConfig(encoded.String(), check); err != nil {
		return out, tomlError(path, encoded.String(), err)
	}
	if hf, ok := check.(*hostsFile); ok {
		nextHosts = hf.Hosts
		for _, host := range hf.Hosts {
			if host.Name == h.Name {
				h = host
			}
		}
	}
	origin, profile := "", ""
	if layer.Kind == api.LayerRepo {
		origin = layer.Name
	}
	if layer.Kind == api.LayerProfile {
		profile = layer.Name
	}
	layers, err := ResolvePolicyLayers(settings, h, origin, profile, api.LayerValues{})
	if err != nil {
		return out, err
	}
	out.Policy, _, err = egress.MergeLayers(layers)
	if err != nil {
		return out, policyError(err)
	}
	if dryRun && layer.Kind == api.LayerHost && remove == "" && rule.Allow {
		tier, hostID, err := HostGrantTier(ctx, grantHost)
		if err != nil {
			return out, err
		}
		outbound, err := policyOutbound(rule.Target)
		if err != nil {
			return out, err
		}
		if err := allowRequestGrant(ctx, grantHost, hostID, tier, outbound, settings); err != nil {
			return out, err
		}
	}
	if !dryRun && changed {
		if layer.Kind == api.LayerHost {
			document, err := hostPolicyDocumentWithSourceContext(ctx, b, encoded.Bytes())
			if err != nil {
				return out, err
			}
			if err := writeHostPolicyDocumentContext(ctx, grantHost, nextHosts, document); err != nil {
				return out, err
			}
		} else if err := writePolicyFile(ctx, path, encoded.Bytes()); err != nil {
			return out, err
		}
	}
	return out, nil
}

func missingFileLayer() error {
	return &UserError{Msg: "choose one policy layer", Hint: "Pass --all, --host, --repo, --profile or --in.", Code: "layer_missing", Kind: KindNeedsAnswer}
}

// policyFileSection follows the shared schema's original spelling. It never
// adds a second case alias beside an existing key.
func policyFileSection(document map[string]any, layer FileLayer) (map[string]any, error) {
	if layer.Kind == api.LayerAll {
		return document, nil
	}
	if layer.Kind == api.LayerHost {
		key := schemaKey(document, reflect.TypeFor[hostsFile](), "host")
		var tables []map[string]any
		switch values := document[key].(type) {
		case []map[string]any:
			tables = values
		case []any:
			for _, v := range values {
				if m, ok := v.(map[string]any); ok {
					tables = append(tables, m)
				}
			}
		}
		for _, table := range tables {
			key := schemaKey(table, reflect.TypeFor[Host](), "name")
			if table[key] == layer.Name {
				return table, nil
			}
		}
		return nil, &UserError{Msg: "no host named " + CleanLine(layer.Name), Hint: brand.Name + " host ls", Code: "host_not_found", Kind: KindNotFound}
	}
	name := "profiles"
	if layer.Kind == api.LayerRepo {
		name = "repos"
	}
	key := schemaKey(document, reflect.TypeFor[Settings](), name)
	parent, ok := document[key].(map[string]any)
	if !ok {
		parent = map[string]any{}
		document[key] = parent
	}
	section, ok := parent[layer.Name].(map[string]any)
	if !ok {
		section = map[string]any{}
		parent[layer.Name] = section
	}
	return section, nil
}

func schemaKey(table map[string]any, typ reflect.Type, want string) string {
	for _, key := range InspectConfigTable(table, typ).Keys {
		if key.Name == want {
			return key.Spelling
		}
	}
	return want
}

func editFileRules(section map[string]any, rule api.Rule, remove string) (bool, int, error) {
	key := schemaKey(section, reflect.TypeFor[api.LayerValues](), "network")
	network, ok := section[key].(map[string]any)
	if !ok {
		network = map[string]any{}
		section[key] = network
	}
	changed, removed, structuredExists, structuredErr := editStructuredFileRules(network, rule, remove)
	if structuredErr != nil {
		return false, 0, structuredErr
	}
	for _, action := range []string{"allow", "deny"} {
		key := schemaKey(network, reflect.TypeFor[api.NetworkValues](), action)
		var values []string
		switch items := network[key].(type) {
		case []string:
			values = items
		case []any:
			for _, v := range items {
				if s, ok := v.(string); ok {
					values = append(values, s)
				}
			}
		}
		kept := make([]string, 0, len(values)+1)
		exists := false
		for _, raw := range values {
			normal, err := egress.ValidateRuleTarget(raw)
			if err != nil {
				return false, 0, policyError(err)
			}
			r := api.Rule{Layer: rule.Layer, Name: rule.Name, Door: api.DoorNet, Allow: action == "allow", Target: normal}
			r.ID = api.RuleID(r)
			if remove != "" && (remove == r.ID || remove == normal || remove == raw) {
				removed++
				changed = true
				continue
			}
			if r.ID == rule.ID {
				exists = true
			}
			kept = append(kept, raw)
		}
		if remove == "" && rule.Methods == nil && (action == "allow") == rule.Allow && !exists && !structuredExists {
			kept = append(kept, rule.Target)
			changed = true
		}
		if len(values) > 0 || len(kept) > 0 {
			network[key] = kept
		}
	}
	if remove != "" && removed == 0 {
		return false, 0, &UserError{Msg: "no matching rule in this layer", Hint: "Run policy ls with the same layer, then pass a rule ID or target.", Code: "not_found", Kind: KindNotFound}
	}
	return changed, removed, nil
}

func writePolicyFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return stoppedBefore("write the policy file", err)
	}
	if len(data) > MaxConfigSize {
		return &UserError{Msg: "the edited policy file is larger than 1 MiB", Hint: "Remove unused policy rules before adding more.", Code: "bad_config", Kind: KindUsage}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".policy-*")
	if err != nil {
		return fmt.Errorf("create the policy file: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("protect the policy file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write the policy file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync the policy file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close the policy file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return stoppedBefore("replace the policy file", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace the policy file: %w", err)
	}
	return nil
}
