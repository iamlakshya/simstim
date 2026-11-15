package api

// FeatureEvents negotiates the actual live events stream, never a polling view.
const FeatureEvents = "events-v1"

// EventsWatchArgs selects one read-only live event scope and its follow mode.
// V must be 1. An empty ID selects this host's actual session producers.
type EventsWatchArgs struct { V int `json:"v"`; ID string `json:"id,omitempty"`; Follow bool `json:"follow"` }
