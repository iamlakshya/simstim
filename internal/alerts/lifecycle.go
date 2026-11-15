package alerts

// Sandbox and host health event kinds use the same dispatcher and noise limits
// as session events. They send only to destinations the user already named.
const (
	SandboxReady    = "sandbox_ready"
	SandboxStopped  = "sandbox_stopped"
	SandboxError    = "sandbox_error"
	HealthDegraded  = "health_degraded"
	HealthRecovered = "health_recovered"
)

func init() {
	Kinds = append(Kinds, SandboxReady, SandboxStopped, SandboxError, HealthDegraded, HealthRecovered)
	DefaultEvents = append(DefaultEvents, SandboxReady, SandboxStopped, SandboxError, HealthDegraded, HealthRecovered)
}
