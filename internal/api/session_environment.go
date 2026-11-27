package api

import "maps"

// SessionEnvironmentV1 is the complete locally resolved ordinary environment.
const SessionEnvironmentV1 = 1

// SessionEnvironment contains no lookup directives or authority. A nil carrier
// is unknown. A supported carrier with a nonnil empty Values map is known empty
// and clears previous ordinary entries. Secret and protected names remain the
// canonical environment validator's responsibility at every producer/consumer.
type SessionEnvironment struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

// Clone owns the complete map without interpreting an absent/future format.
func (e *SessionEnvironment) Clone() *SessionEnvironment {
	if e == nil {
		return nil
	}
	return &SessionEnvironment{Version: e.Version, Values: maps.Clone(e.Values)}
}
