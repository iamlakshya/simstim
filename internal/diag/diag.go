// Package diag writes debug traces: records of when keys, output, pings and
// memory samples happen, as JSON lines in a file, so that a slow session can
// be measured (docs/contributing/debug-trace.md).
//
// A record holds a monotonic time, a category, an event, and sizes and times
// only. It never holds key bytes, output bytes, screen text, secrets or the
// environment.
package diag

import (
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// EnvFile is the environment variable that turns on the client's trace. Its
// value is the path of the file to write.
var EnvFile = brand.Env("DEBUG_FILE")

// The categories of records.
const (
	CatAttach = "attach" // keys, output, and the holder's lock
	CatNet    = "net"    // pings
	CatMemory = "memory" // samples of the session's memory and pressure
)

// Record is one line of a trace file. Times are monotonic times from Now,
// and durations are in ns. A field that does not apply is left out.
type Record struct {
	// T is the time of the event: for most, when it ended. A read of the
	// agent's output and a status tick have the time they started.
	T   int64  `json:"t"`
	Cat string `json:"cat"`
	Ev  string `json:"ev"`

	// On a start record: the side that wrote the file ("client" or "host"),
	// the session, the version and the wall clock, as Unix time in ns.
	Side    string `json:"side,omitempty"`
	Session string `json:"session,omitempty"`
	Version string `json:"version,omitempty"`
	Wall    int64  `json:"wall,omitempty"`
	// HostTrace is true on a client's conn record when the host helper
	// writes a trace of its own.
	HostTrace bool `json:"host_trace,omitempty"`

	// Trace names one attach: one connection from the client to a holder.
	// Key numbers count from 1 in each.
	Trace string `json:"trace,omitempty"`
	Key   int64  `json:"key,omitempty"`
	// Read is the client's read from stdin, counted from 1 in each attach.
	Read int64 `json:"read,omitempty"`
	// N is a size in bytes.
	N int `json:"n,omitempty"`
	// Recv is when a frame arrived, and Bridge is when the bridge read a key.
	Recv   int64 `json:"recv,omitempty"`
	Bridge int64 `json:"bridge,omitempty"`
	// Wait is the time spent waiting for the holder's lock, and Hold the
	// time the lock was held.
	Wait int64 `json:"wait,omitempty"`
	Hold int64 `json:"hold,omitempty"`
	// Feed is the time in the holder's screen model, and Write the time in a
	// write.
	Feed  int64 `json:"feed,omitempty"`
	Write int64 `json:"write,omitempty"`

	// A ping: its number, the round trip, the host's time in the pong (0
	// from an older host helper), and the host's clock minus the client's.
	Ping   uint64 `json:"ping,omitempty"`
	RTT    int64  `json:"rtt,omitempty"`
	Host   int64  `json:"host,omitempty"`
	Offset int64  `json:"offset,omitempty"`

	Mem *Memory `json:"mem,omitempty"`

	// Dropped counts the records lost because the queue was full.
	Dropped int64 `json:"dropped,omitempty"`
}

// Memory is one sample of a session unit's cgroup files. Sizes are in
// bytes, the pressure totals in ns, and the rest are counts since the unit
// started.
type Memory struct {
	Current int64 `json:"current"`
	Peak    int64 `json:"peak,omitempty"`
	// From memory.stat.
	Anon     int64 `json:"anon"`
	File     int64 `json:"file"`
	Refault  int64 `json:"workingset_refault_file"`
	MajFault int64 `json:"pgmajfault"`
	// From memory.events.
	High    int64 `json:"high"`
	Max     int64 `json:"max"`
	OOM     int64 `json:"oom"`
	OOMKill int64 `json:"oom_kill"`
	// The "total" of each line of memory.pressure and cpu.pressure.
	MemSome int64 `json:"mem_some"`
	MemFull int64 `json:"mem_full"`
	CPUSome int64 `json:"cpu_some"`
	CPUFull int64 `json:"cpu_full"`
}

var (
	start    = time.Now()
	startSys = sysMono()
)

// Now is the monotonic time in ns. On Linux and macOS it is the system's
// CLOCK_MONOTONIC, so the bridge and the holder on one host share it. Go's
// own monotonic clock counts from the start of each process.
func Now() int64 { return startSys + int64(time.Since(start)) }
