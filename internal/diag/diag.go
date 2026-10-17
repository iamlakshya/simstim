// Package diag is the debug mode's common part: records of when keys,
// output, pings and memory samples happen, as JSON lines in a file, debug
// lines, Go's runtime metrics, profiles and the flight recorder
// (docs/contributing/debug-trace.md, decision 0025).
//
// A record holds a monotonic time, a category, an event, and sizes, times
// and names only. It never holds key bytes, output bytes, screen text,
// secrets, the environment or a command line.
package diag

import (
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// EnvFile is the environment variable that turns on the client's trace. Its
// value is the path of the file to write.
var EnvFile = brand.Env("DEBUG_FILE")

// Events that every file may hold, whatever its categories: they are about
// debug itself.
const (
	EvStart   = "start"   // debug started
	EvStop    = "stop"    // debug stopped, with its reason
	EvSave    = "save"    // a profile or a window of the flight recorder was written
	EvDropped = "dropped" // records were lost
	EvLog     = "log"     // a debug line
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

	// Cats are the categories on, on a start record.
	Cats string `json:"cats,omitempty"`
	// Level and Text are a debug line's: its level, and the line as slog's
	// text handler writes it, without the time and the level.
	Level string `json:"level,omitempty"`
	Text  string `json:"text,omitempty"`
	// Reason says why: a stop, a save, or a reconnect.
	Reason string `json:"reason,omitempty"`
	// Op is a request's name, and Dur how long it took.
	Op  string `json:"op,omitempty"`
	Dur int64  `json:"dur,omitempty"`
	// File is the name of a file that a save wrote, in its folder.
	File string `json:"file,omitempty"`
	// Running and PID are the state of the shared SSH connection's master.
	Running *bool `json:"running,omitempty"`
	PID     int   `json:"pid,omitempty"`
	// Msg is the Go type of a launcher's message, and Update and View the
	// time a frame spent in each.
	Msg    string `json:"msg,omitempty"`
	Update int64  `json:"update,omitempty"`
	View   int64  `json:"view,omitempty"`
	// Procs are the largest processes left in a session's unit after an
	// out-of-memory kill. Before are the largest at the start of the memory
	// warning before it, taken at BeforeT, which name the process killed.
	Procs   []Proc `json:"procs,omitempty"`
	Before  []Proc `json:"before,omitempty"`
	BeforeT int64  `json:"before_t,omitempty"`
	// RT is one second of Go's runtime metrics.
	RT *Metrics `json:"rt,omitempty"`
}

// Proc is one process of a session's unit: its name from /proc/<pid>/comm,
// never its command line, and its memory, in bytes.
type Proc struct {
	PID  int    `json:"pid"`
	Comm string `json:"comm"`
	RSS  int64  `json:"rss"`
	PSS  int64  `json:"pss"`
}

// Metrics is one second of Go's runtime metrics in one process. Times are
// in ns, and each is the upper bound of a bucket of the runtime's histogram.
type Metrics struct {
	// HeapObjects is the memory of the heap's objects, in bytes.
	HeapObjects int64 `json:"heap_objects"`
	// GCPauses counts the pauses for garbage collection in the second, and
	// GCPauseMax is the longest.
	GCPauses   int64 `json:"gc_pauses"`
	GCPauseMax int64 `json:"gc_pause_max,omitempty"`
	// SchedP99 and SchedMax are how long goroutines waited for a CPU in
	// the second: the 99th percentile and the longest.
	SchedP99 int64 `json:"sched_p99,omitempty"`
	SchedMax int64 `json:"sched_max,omitempty"`
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
func Now() int64 {
	if perCallMono {
		return sysMono()
	}
	return startSys + int64(time.Since(start))
}
