package tui

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// networkHoldView retains display state for one externally owned session/
// egress lifetime. It is never a target, approval or lifetime constructor.
// Its real owner must retire it on actor stop, cold start or source change.
type networkHoldView struct {
	snapshot    api.NetworkHoldSnapshot
	selected    string
	loaded      bool
	dirty       bool
	invalidated uint64
}

func (v *networkHoldView) invalidate(revision uint64) bool {
	if revision == 0 || revision <= v.invalidated || v.loaded && revision <= v.snapshot.Revision {
		return false
	}
	v.invalidated, v.dirty = revision, true
	return true
}

// apply accepts only a complete snapshot. A notification never fabricates a
// question, and an older reply cannot replace a newer revision or selection.
func (v *networkHoldView) apply(snapshot api.NetworkHoldSnapshot) bool {
	if !api.ValidNetworkHoldSnapshot(snapshot) || snapshot.Revision < v.invalidated || v.loaded && (snapshot.Revision < v.snapshot.Revision || v.snapshot.Stopped && !snapshot.Stopped) {
		return false
	}
	copy := snapshot
	copy.Requests = slices.Clone(snapshot.Requests)
	v.snapshot, v.loaded, v.dirty, v.invalidated = copy, true, false, copy.Revision
	if copy.Stopped {
		v.selected = ""
		return true
	}
	if slices.ContainsFunc(copy.Requests, func(request api.PendingNetworkRequest) bool { return request.ID == v.selected }) {
		return true
	}
	v.selected = ""
	if len(copy.Requests) > 0 {
		v.selected = copy.Requests[0].ID
	}
	return true
}

// current returns the original opaque descriptor for row selection only.
// A caller still must send its unchanged ID to the actual backend resolver.
func (v *networkHoldView) current() (api.PendingNetworkRequest, bool) {
	if !v.loaded || v.snapshot.Stopped {
		return api.PendingNetworkRequest{}, false
	}
	for _, request := range v.snapshot.Requests {
		if request.ID == v.selected {
			return request, true
		}
	}
	return api.PendingNetworkRequest{}, false
}
