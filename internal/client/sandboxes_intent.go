package client

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
)

type savedCreationIntent struct {
	Level   string   `json:"level"`
	Exposed []string `json:"exposed,omitempty"`
}

func validCreationWall(level string) bool {
	return level == "none" || level == "process" || level == "gvisor" || level == "microvm"
}

func normalizeCreationIntent(p SandboxPolicy) (SandboxPolicy, error) {
	if p.CreationWall == "" {
		p.CreationWall = "none" // Old internal unwalled start callers.
	}
	if !validCreationWall(p.CreationWall) || p.SSHAgent && p.CreationWall != "none" {
		return p, badSandboxState()
	}
	p.Exposed = slices.Clone(p.Exposed)
	sort.Strings(p.Exposed)
	for i, name := range p.Exposed {
		if !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" || i > 0 && p.Exposed[i-1] == name {
			return p, badSandboxState()
		}
	}
	return p, nil
}

func decodeCreationIntent(raw json.RawMessage) (savedCreationIntent, error) {
	return decodeCreationIntentContext(context.Background(), raw)
}

func decodeCreationIntentContext(ctx context.Context, raw json.RawMessage) (savedCreationIntent, error) {
	if err := ctx.Err(); err != nil {
		return savedCreationIntent{}, err
	}
	var intent savedCreationIntent
	fields, err := sandboxObjectContext(ctx, raw, "level", "exposed")
	if err != nil {
		return intent, err
	}
	if json.Unmarshal(fields["level"], &intent.Level) != nil || !validCreationWall(intent.Level) {
		return intent, badSandboxStateContext(ctx)
	}
	if exposed, ok := fields["exposed"]; ok {
		if bytes.Equal(bytes.TrimSpace(exposed), []byte("null")) || json.Unmarshal(exposed, &intent.Exposed) != nil {
			return intent, badSandboxStateContext(ctx)
		}
	}
	if err := ctx.Err(); err != nil {
		return intent, err
	}
	for i, name := range intent.Exposed {
		if err := ctx.Err(); err != nil {
			return intent, err
		}
		if !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" || i > 0 && intent.Exposed[i-1] >= name {
			return intent, badSandboxStateContext(ctx)
		}
	}
	return intent, nil
}

// sandboxObject rejects duplicate and aliased known names. Unknown values
// remain raw, including future nested metadata and its exact value bytes.
func sandboxObject(raw []byte, known ...string) (map[string]json.RawMessage, error) {
	return sandboxObjectContext(context.Background(), raw, known...)
}

func sandboxObjectContext(ctx context.Context, raw []byte, known ...string) (map[string]json.RawMessage, error) {
	fields, err := api.ProjectedJSONObjectFields(ctx, raw, known...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, badSandboxState()
	}
	return fields, nil
}

// marshalSandboxObject preserves raw values instead of making the JSON
// encoder compact or re-indent fields owned by a newer client.
func marshalSandboxObject(fields map[string]json.RawMessage) ([]byte, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, name := range names {
		if !json.Valid(fields[name]) {
			return nil, badSandboxState()
		}
		if i != 0 {
			out.WriteByte(',')
		}
		key, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(fields[name])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func upgradeSandboxIntent(f *sandboxFile) error {
	return upgradeSandboxIntentContext(context.Background(), f)
}

func upgradeSandboxIntentContext(ctx context.Context, f *sandboxFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.schema == 2 || f.schema == 3 || f.schema == 4 {
		return nil
	}
	if f.schema != 1 {
		return badSandboxState()
	}
	for id, raw := range f.sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Only this known pre-wall local schema may become legacy none.
		if _, err := decodeSandboxContext(ctx, id, raw, 1); err != nil {
			return err
		}
		fields, err := sandboxObjectContext(ctx, raw)
		if err != nil {
			return err
		}
		fields["wall_intent"] = json.RawMessage(`{"level":"none"}`)
		f.sessions[id], err = marshalSandboxObject(fields)
		if err != nil {
			return err
		}
	}
	f.schema, f.root["schema"] = 2, json.RawMessage("2")
	return nil
}
