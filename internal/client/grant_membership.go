package client

import (
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func cloneGrantSnapshot(s *api.SandboxSnapshot) *api.SandboxSnapshot {
	return s.Clone()
}

func sameGrantSnapshot(a, b *api.SandboxSnapshot) bool {
	return a != nil && b != nil && a.Members != nil && b.Members != nil && a.Version == b.Version && a.Primary == b.Primary &&
		a.Generation == b.Generation && slices.Equal(a.Members, b.Members)
}

func checkSnapshotAuthority(snapshot *api.SandboxSnapshot, original string, p *SandboxPolicy) error {
	if snapshot == nil || snapshot.Check(original, proto.MaxFrame) != nil {
		return joinIntentError()
	}
	if p != nil && !p.FutureLineage && localSandboxRoot(p) != snapshot.Primary {
		return joinIntentError()
	}
	return nil
}

func sameGrantNames(a, b []string) bool {
	return slices.Equal(a, b)
}

func cloneGrantHost(h Host) Host {
	h.LayerValues = api.CloneLayerValues(h.LayerValues)
	if h.Lock != nil {
		values := api.CloneLayerValues(*h.Lock)
		h.Lock = &values
	}
	h.Labels = maps.Clone(h.Labels)
	h.Secrets, h.Repos = slices.Clone(h.Secrets), slices.Clone(h.Repos)
	if h.Provider != nil {
		v := *h.Provider
		h.Provider = &v
	}
	if h.Machine != nil {
		v := *h.Machine
		v.Source = CloneMachineSource(h.Machine.Source)
		h.Machine = &v
	}
	if h.pool != nil {
		v := *h.pool
		h.pool = &v
	}
	return h
}
