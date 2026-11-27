package supervisor

import (
	"context"
	"crypto/rand"
	"io"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// prepareBootDecoys owns only this actual cold start. Policy edits cannot
// replace its applied generation, files or registration before another boot.
func (s *Supervisor) prepareBootDecoys(ctx context.Context) error {
	s.mu.Lock()
	if s.bootDecoysPrepared {
		s.mu.Unlock()
		return nil
	}
	if s.m.Wall == nil || s.m.Wall.Level == "none" {
		s.bootDecoysPrepared = true
		s.mu.Unlock()
		return nil
	}
	layers := api.CloneLayers(s.m.Layers)
	s.mu.Unlock()
	policy, cleaned, files, tripwires, err := resolveBootDecoys(ctx, layers, rand.Reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bootDecoysPrepared {
		return execRefusal("this cold-start decoy generation already exists")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.bootDecoyFiles, s.bootDecoyTripwires = files, tripwires
	// Restore records from an earlier resolver version still pass through the
	// ONE current canonical merge before the applied egress start is built.
	s.m.Policy, s.m.Layers = &policy, cleaned
	s.bootDecoysPrepared = true
	return nil
}

func bootDecoyPlan(ctx context.Context, layers []api.Layer, entropy io.Reader) (*proto.DecoyFilesV1, []api.DecoyTripwire, error) {
	_, _, files, tripwires, err := resolveBootDecoys(ctx, layers, entropy)
	return files, tripwires, err
}

func resolveBootDecoys(ctx context.Context, layers []api.Layer, entropy io.Reader) (api.Policy, []api.Layer, *proto.DecoyFilesV1, []api.DecoyTripwire, error) {
	if err := ctx.Err(); err != nil {
		return api.Policy{}, nil, nil, nil, err
	}
	// The canonical resolver checks every lock before false dominance. It
	// returns before entropy, guest files or Init.Decoys can be constructed.
	policy, cleaned, err := egress.MergeLayers(layers)
	if err != nil {
		return api.Policy{}, nil, nil, nil, err
	}
	enabled := true
	if policy.Sandbox.Decoys != nil {
		enabled = *policy.Sandbox.Decoys
	}
	files, tripwires, err := makeDecoys(ctx, enabled, entropy)
	if err != nil {
		return api.Policy{}, nil, nil, nil, err
	}
	return policy, cleaned, files, tripwires, nil
}

func (s *Supervisor) bootDecoyArgs(args *proto.CtlStartArgs) {
	root := s.owner()
	root.mu.Lock()
	args.Decoys = proto.CloneDecoyFilesV1(root.bootDecoyFiles)
	root.mu.Unlock()
}

func (s *Supervisor) requireDecoyGuest(ctx context.Context) error {
	if s.bootDecoyFiles == nil {
		return nil
	}
	var features proto.CtlGuestFeatures
	if err := s.ctl.request(ctx, proto.CtlGuestCapabilities, struct{}{}, &features); err != nil {
		return err
	}
	for _, feature := range features.Features {
		if features.Version == 1 && feature == proto.FeatureDecoyFilesV1 {
			return nil
		}
	}
	return execRefusal("the actual guest cannot install the private decoy generation")
}

// noteDecoyUse receives only the existing native egress audit after its
// scrubbed record was written. A guest event or public label cannot call it.
func (s *Supervisor) noteDecoyUse(entry api.NetAuditEntry) {
	if entry.Rule != "decoy" || entry.Decision != "deny" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	known := false
	for _, tripwire := range s.bootDecoyTripwires {
		if tripwire.Name == entry.Secret {
			known = true
			break
		}
	}
	if !known {
		return
	}
	s.decoyUsed = true
	if s.rec.phase == host.LifeRunning {
		if s.m.Status.Source != host.SrcMemory {
			s.m.Status.Reason = "decoy used"
		}
		s.dirty = true
	}
	event := s.alertEvent(alerts.SecretMisuse)
	event.Detail = "decoy " + entry.Secret + ", sent to " + entry.Host
	s.raise(event)
}
