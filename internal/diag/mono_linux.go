//go:build linux

package diag

// A common kernel clock must be read at the event. Separate process-start
// anchors can include different startup preemption and cannot bound a key's
// sub-millisecond hop between the bridge, supervisor and holder.
const perCallMono = true
