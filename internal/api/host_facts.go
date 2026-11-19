package api

import "time"

// HostFactsVersion is the read-only host.facts wire format. Facts cannot
// reserve capacity or issue an operation, runtime, stop or native boot grant.
const HostFactsVersion = 1

// Health describes an observed host condition. Levels lists the affected wall
// levels. A health row never changes a sandbox's stored minimum level.
type Health struct {
	ID string `json:"id"`
	// Severity is ok, warn or blocks_new.
	Severity string   `json:"severity"`
	Levels   []string `json:"levels,omitempty"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint"`
}

// HostFacts contains native observations and advisory cached metadata for
// host selection. All memory and disk quantities are bytes. Pressure values
// are Linux PSI "some" avg10 percentages, not utilisation or free capacity.
// Each Known bit distinguishes an observed zero from unavailable evidence.
// A client records its own receipt time on the same accepted source/HostID;
// At is an observation timestamp, not a clock or freshness authority.
// Actual New always performs fresh admission under the host's placement lock.
type HostFacts struct {
	Version         int         `json:"v"`
	HostID          string      `json:"host_id"`
	BootID          string      `json:"boot_id"`
	At              time.Time   `json:"at"`
	OS              string      `json:"os"`
	Arch            string      `json:"arch"`
	Level           string      `json:"level"`
	LevelKnown      bool        `json:"level_known"`
	MemTotal        int64       `json:"mem_total"`
	MemForSessions  int64       `json:"mem_for_sessions"`
	MemReserved     int64       `json:"mem_reserved"`
	Room            int64       `json:"room"`
	MemoryKnown     bool        `json:"memory_known"`
	AccountingKnown bool        `json:"accounting_known"`
	Sandboxes       int         `json:"sandboxes"`
	DiskFree        int64       `json:"disk_free"`
	DiskKnown       bool        `json:"disk_known"`
	Walls           []WallProbe `json:"walls"`
	WallsKnown      bool        `json:"walls_known"`
	// Cached digests describe observed cache records only. They do not prove
	// physical readiness or replace the original signed runtime delivery.
	RuntimeDigests      []string `json:"runtime_digests"`
	RuntimeDigestsKnown bool     `json:"runtime_digests_known"`
	ImageDigests        []string `json:"image_digests"`
	ImageDigestsKnown   bool     `json:"image_digests_known"`
	PressureMem         float64  `json:"pressure_mem"`
	PressureMemKnown    bool     `json:"pressure_mem_known"`
	PressureCPU         float64  `json:"pressure_cpu"`
	PressureCPUKnown    bool     `json:"pressure_cpu_known"`
	OnBattery           bool     `json:"on_battery"`
	OnBatteryKnown      bool     `json:"on_battery_known"`
	Drain               bool     `json:"drain"`
	DrainKnown          bool     `json:"drain_known"`
	Health              []Health `json:"health"`
}
