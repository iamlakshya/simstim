package api

// OpDebugTraceUpperV1 reads bounded original supervisor observations. They are
// diagnostic data and carry no runtime, process, file or operation authority.
const OpDebugTraceUpperV1 = "debug.trace.upper.v1"

// TraceUpperSampleV1 bounds one controlled key's bridge-to-PTY path by the
// last complete output frame observed in the SAME host clock domain.
type TraceUpperSampleV1 struct {
	Trace    string `json:"trace"`
	Key      int64  `json:"key"`
	Bridge   int64  `json:"bridge"`
	Received int64  `json:"received"`
	End      int64  `json:"end"`
	Frames   int    `json:"frames"`
}

// TraceUpperSnapshotV1 is a copy of the actual supervisor's bounded ring.
// Last-frame bounds are valid only with the acknowledged controlled fixture;
// arbitrary terminal output does not establish a key's completion.
type TraceUpperSnapshotV1 struct {
	Version    int                  `json:"v"`
	Session    string               `json:"session"`
	Generation string               `json:"generation"`
	Dropped    bool                 `json:"dropped"`
	Samples    []TraceUpperSampleV1 `json:"samples"`
}
