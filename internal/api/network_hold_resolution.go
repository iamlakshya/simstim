package api

import "time"

// NetworkHoldResolution is a transient, non-sensitive target resolved by the
// current backend from a live opaque request ID. It grants nothing. A source-
// sensitive target must return an error instead of constructing this result.
// The original local permission/frame checks still guard any later file write.
type NetworkHoldResolution struct {
	Version  int                    `json:"v"`
	Request  string                 `json:"request"`
	Scope    NetworkHoldScope       `json:"scope"`
	Target   string                 `json:"target"`
	Expires  time.Time              `json:"expires"`
	Revision uint64                 `json:"revision"`
	Class    NetworkHoldTargetClass `json:"class"`
}
