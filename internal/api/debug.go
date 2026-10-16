package api

import "time"

// The debug mode's requests (decision 0025). The bridge serves debug.start
// and debug.stop for itself when ID is empty, and passes a session's to its
// holder, with debug.profile and debug.save.
const (
	OpDebugStart   = "debug.start"
	OpDebugStop    = "debug.stop"
	OpDebugProfile = "debug.profile"
	OpDebugSave    = "debug.save"
)

// FeatureDebug is the feature of a host helper that serves the debug
// requests. It implies FeatureTrace.
const FeatureDebug = "debug"

// FeatureTrace is part 1's feature: an attach may carry a trace ID.
const FeatureTrace = "trace"

// CodeBusy is the error code of a profile or a save while another runs in
// the same holder.
const CodeBusy = "busy"

// The time limits of a session's debug: by default, and at most.
const (
	DebugFor    = 30 * time.Minute
	DebugForMax = 24 * time.Hour
)

// DebugStartArgs turns on the debug of a session, or of the bridge itself
// when ID is empty.
type DebugStartArgs struct {
	ID         string   `json:"id,omitempty"`
	Categories []string `json:"categories"`
	// Seconds is the session's time limit: DebugFor when 0.
	Seconds int `json:"seconds,omitempty"`
}

// DebugState is the answer to debug.start and debug.stop.
type DebugState struct {
	// Categories are the ones on, which the side that answers records.
	Categories []string `json:"categories,omitempty"`
	// Until is when a session's debug stops by itself.
	Until *time.Time `json:"until,omitempty"`
	// File is the debug file on the host.
	File string `json:"file,omitempty"`
	// Stopped is true when debug.stop ended a debug that was on.
	Stopped bool `json:"stopped,omitempty"`
}

// DebugProfileArgs asks a session's holder for a profile.
type DebugProfileArgs struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Seconds is the time of a CPU profile.
	Seconds int `json:"seconds,omitempty"`
}

// DebugFile names a file that the host wrote, and its size.
type DebugFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
