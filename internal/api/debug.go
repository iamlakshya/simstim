package api

import "time"

// The debug mode's requests (decision 0025). The bridge serves debug.start
// and debug.stop for itself when ID is empty, and passes a session's to its
// holder, with debug.profile and debug.save. It serves debug.collect and
// debug.read itself, for the bundle.
const (
	OpDebugStart   = "debug.start"
	OpDebugStop    = "debug.stop"
	OpDebugProfile = "debug.profile"
	OpDebugSave    = "debug.save"
	OpDebugCollect = "debug.collect"
	OpDebugRead    = "debug.read"
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

// DebugCollectArgs asks the bridge for the host's part of a bundle: the
// files of the sessions IDs, or with All, of every session that has debug
// files. The bridge's own debug files come either way.
type DebugCollectArgs struct {
	IDs []string `json:"ids,omitempty"`
	All bool     `json:"all,omitempty"`
}

// DebugCollected is the host's part of a bundle: the files that debug.read
// may send, and what the bridge left out.
type DebugCollected struct {
	// Home is $HOME on the host, for the client's replacements.
	Home string `json:"home"`
	// Files are the files to read, by their path in the bundle, such as
	// host/sessions/<id>/debug.jsonl, with their size now.
	Files []DebugFile `json:"files"`
	// Notes say what the bridge left out, and why.
	Notes []string `json:"notes,omitempty"`
}

// DebugReadMax is the most that one debug.read sends: 1 MiB, or about
// 1.4 MiB in base64, well inside one frame.
const DebugReadMax = 1 << 20

// DebugReadArgs asks for part of a file that debug.collect listed, from
// Offset. Name is the file's path in the bundle, never a path on the host.
type DebugReadArgs struct {
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
}

// DebugChunk is part of a file, after the host replaced the values of its
// alerts file in it.
type DebugChunk struct {
	Data []byte `json:"data"`
	// Next is the offset of the next part, in the file on the host.
	Next int64 `json:"next"`
	// EOF is true when Data ends the file.
	EOF bool `json:"eof,omitempty"`
	// Hidden counts the alert tokens, addresses and header values that the
	// host replaced in Data.
	Hidden int `json:"hidden,omitempty"`
}
