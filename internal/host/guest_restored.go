package host

import "time"

// MarkRestored marks only the guest-local holder as a restore, not a host fact.
func MarkRestored(m *Manifest) {
	m.Labels = withLabel(m.Labels, restoredLabel, time.Now().UTC().Format(time.RFC3339Nano))
}
