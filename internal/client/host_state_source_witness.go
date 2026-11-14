package client

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

var hostStateSourceWitnessFields = []string{"version", "exists", "generation", "path", "root"}
var hostStateSourceRootFields = []string{"version", "identity", "mode", "size", "modified", "directory"}

// hostStateSourceWitness is serialisable historical provenance, NOT permission.
// Only actual Conn/OP/source checks can use it at ACK commit or recovery.
type hostStateSourceWitness struct {
	Version    int               `json:"version"`
	Exists     bool              `json:"exists"`
	Generation string            `json:"generation"`
	Path       string            `json:"path"`
	Root       cachefs.Reference `json:"root"`
	extra      map[string]json.RawMessage
	future     json.RawMessage
}

func (w hostStateSourceWitness) validate() error {
	if w.Version != 1 || !w.Exists || !ValidHostID(w.Generation) || w.Path == "" ||
		!w.Root.Known() || !w.Root.Directory || len(w.extra) != 0 || len(w.future) != 0 {
		return hostStateRemovalError("the original Source witness is unknown or has no saved membership")
	}
	return nil
}

func (w hostStateSourceWitness) clone() hostStateSourceWitness {
	w.Root.Modified = w.Root.Modified.UTC()
	w.future = slices.Clone(w.future)
	w.extra = maps.Clone(w.extra)
	for key, raw := range w.extra {
		w.extra[key] = slices.Clone(raw)
	}
	// Reference's future fields are opaque, private and immutable. A value
	// copy preserves their unknown status; none are zeroed or made Known.
	return w
}

func captureHostStateSourceWitness(ctx context.Context, c *Conn) (hostStateSourceWitness, error) {
	var witness hostStateSourceWitness
	if ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostMutationContextKey{}) != nil || ctx.Value(hostForgetMembershipKey{}) != nil {
		return witness, hostStateRemovalError("receipt Source must be checked before local writer callbacks")
	}
	if c == nil || !ValidHostID(c.HostID()) {
		return witness, hostStateRemovalError("a Source receipt requires an actual checked modern connection")
	}
	if err := c.BindHostStateObservation(ctx); err != nil {
		return witness, err
	}
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil || o.role != hostStateSaved || !o.source.exists {
		return witness, hostStateRemovalError("a Source receipt cannot promote a registration or provisional connection")
	}
	o.mu.Lock()
	checked := o.checked && o.checkedConn == c && o.hostID == c.HostID() && o.legacyConn == nil
	o.mu.Unlock()
	if !checked {
		return witness, hostStateRemovalError("the Source receipt has no same checked connection observation")
	}
	witness = hostStateSourceWitness{Version: 1, Exists: true, Generation: o.source.generation, Path: o.source.path, Root: o.source.root}
	witness.Root.Modified = witness.Root.Modified.UTC()
	return witness, witness.validate()
}

func (w hostStateSourceWitness) check(ctx context.Context, c *Conn) error {
	if err := w.validate(); err != nil {
		return err
	}
	actual, err := captureHostStateSourceWitness(ctx, c)
	if err != nil {
		return err
	}
	original := hostStateSource{exists: w.Exists, generation: w.Generation, path: w.Path, root: w.Root}
	current := hostStateSource{exists: actual.Exists, generation: actual.Generation, path: actual.Path, root: actual.Root}
	if !sameHostTransportSource(original, current) {
		return hostStateRemovalError("the receipt belongs to another original membership Source")
	}
	return nil
}

// This byte adapter delegates JSON field shape to the ONE canonical scanner
// and native Reference values to cachefs. Main's shared FD row projection uses
// these field sets without materialising opaque unselected receipt history.
func (w *hostStateSourceWitness) UnmarshalJSON(raw []byte) error {
	decoded, err := decodeHostStateSourceWitnessContext(context.Background(), raw)
	if err == nil {
		*w = decoded
	}
	return err
}

func decodeHostStateSourceWitnessContext(ctx context.Context, raw []byte) (hostStateSourceWitness, error) {
	var witness hostStateSourceWitness
	fields, err := api.ProjectedJSONObjectFields(ctx, raw, hostStateSourceWitnessFields...)
	if err != nil {
		return witness, err
	}
	if err := json.Unmarshal(fields["version"], &witness.Version); err != nil {
		return witness, err
	}
	if witness.Version != 1 {
		witness.future = slices.Clone(raw)
		return witness, ctx.Err()
	}
	if _, err := api.ProjectedJSONObjectFields(ctx, fields["root"], hostStateSourceRootFields...); err != nil {
		return witness, err
	}
	type plain hostStateSourceWitness
	var decoded plain
	if err := decodeContextJSON(ctx, raw, &decoded); err != nil {
		return witness, err
	}
	witness = hostStateSourceWitness(decoded)
	for _, key := range hostStateSourceWitnessFields {
		delete(fields, key)
	}
	witness.extra = fields
	return witness, ctx.Err()
}

func (w hostStateSourceWitness) MarshalJSON() ([]byte, error) {
	if w.future != nil {
		return slices.Clone(w.future), nil
	}
	type plain hostStateSourceWitness
	raw, err := json.Marshal(plain(w))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for key, value := range w.extra {
		if _, known := fields[key]; !known {
			fields[key] = value
		}
	}
	return marshalSandboxObject(fields)
}
