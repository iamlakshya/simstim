//go:build !linux

package host

import "context"

// EventsDiscovery names the native discovery owner on supported helpers.
type EventsDiscovery struct{}

// EventsDiscoverySupported reports whether the helper has native discovery.
func EventsDiscoverySupported() bool { return false }

// OpenEventsDiscovery refuses a host-wide watch without a native source.
func OpenEventsDiscovery() (*EventsDiscovery, error) {
	return nil, UserError("events_unavailable", "this host has no native session discovery", "Use events for one session, or use a Linux host for a host-wide watch.")
}

// Next cannot produce native notifications on this platform.
func (*EventsDiscovery) Next(context.Context) error {
	return UserError("events_unavailable", "this host has no native session discovery", "Use events for one session, or use a Linux host for a host-wide watch.")
}

// Close has no native resource to release on this platform.
func (*EventsDiscovery) Close() error { return nil }
