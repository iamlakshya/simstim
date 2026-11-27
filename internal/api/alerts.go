package api

import "time"

// AlertsConfig is a host's alerts file (alerts.toml): which session events to
// send, and where. An empty field takes its default.
type AlertsConfig struct {
	// Events lists the events to send, for example ["needs_input", "failed"].
	Events []string `json:"events,omitempty" toml:"events,omitempty"`
	// Delay is how long needs_input waits before it is sent, as "10s".
	Delay string `json:"delay,omitempty" toml:"delay,omitempty"`
	// MinTurn is the shortest turn that sends turn_done, as "60s".
	MinTurn string `json:"min_turn,omitempty" toml:"min_turn,omitempty"`
	// Detail adds the agent's question to events. It is off by default,
	// because the question can hold code.
	Detail *bool `json:"detail,omitempty" toml:"detail,omitempty"`
	// QuietWhenTyping drops needs_input when an attached client typed during
	// the delay. It is on by default.
	QuietWhenTyping *bool `json:"quiet_when_typing,omitempty" toml:"quiet_when_typing,omitempty"`
	// Destinations are where events go. There is no default destination.
	Destinations []AlertDestination `json:"destinations,omitempty" toml:"destination,omitempty"`
}

// AlertDestination is one place events go: a command, an ntfy topic or a webhook.
type AlertDestination struct {
	// Type is "command", "ntfy" or "webhook".
	Type string `json:"type" toml:"type"`
	// Events, when set, replaces the global list for this destination.
	Events []string `json:"events,omitempty" toml:"events,omitempty"`
	// Run is the command line of a command destination, run with sh -c. It
	// gets the event as JSON on stdin and as environment variables.
	Run string `json:"run,omitempty" toml:"run,omitempty"`
	// URL is the ntfy topic or the webhook address.
	URL string `json:"url,omitempty" toml:"url,omitempty"`
	// Token is an ntfy access token, sent as a bearer token.
	Token string `json:"token,omitempty" toml:"token,omitempty"`
	// TokenFile is a file that holds the ntfy token. Only its owner may read it.
	TokenFile string `json:"token_file,omitempty" toml:"token_file,omitempty"`
	// Priority is the ntfy priority: 1 to 5, or min, low, default, high, urgent.
	Priority string `json:"priority,omitempty" toml:"priority,omitempty"`
	// Format is the webhook's body: "json", "slack", "discord" or "otlp".
	Format string `json:"format,omitempty" toml:"format,omitempty"`
	// Headers are extra HTTP headers for a webhook.
	Headers map[string]string `json:"headers,omitempty" toml:"headers,omitempty"`
}

// AlertsInfo describes a host's alerts file.
type AlertsInfo struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	// Config is the file's config, with its defaults filled in and its
	// secrets hidden.
	Config *AlertsConfig `json:"config,omitempty"`
	// Error says why the file cannot be used, and Hint what to do about it.
	Error string `json:"error,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

// AlertsSetArgs writes a host's alerts file.
type AlertsSetArgs struct {
	Config AlertsConfig `json:"config"`
	// Add keeps the file's destinations and adds Config's, and changes only
	// the settings Config sets. Without it, Config replaces the whole file.
	Add bool `json:"add,omitempty"`
}

// AlertsTestArgs sends a test event to every destination.
type AlertsTestArgs struct {
	// Event is the kind of event to send; "test" when empty.
	Event string `json:"event,omitempty"`
}

// AlertResult is what one destination did with an event.
type AlertResult struct {
	Type   string `json:"type"`
	Target string `json:"target"` // the address with its secret parts hidden, or the command
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	MS     int64  `json:"ms"`
}

// AlertsLogArgs asks for the newest lines of the delivery log.
type AlertsLogArgs struct {
	Lines int `json:"lines,omitempty"`
}

// AlertLogEntry is one line of a host's alert delivery log.
type AlertLogEntry struct {
	Time    time.Time `json:"time"`
	Event   string    `json:"event"`
	Session string    `json:"session,omitempty"` // the session's name
	ID      string    `json:"id,omitempty"`      // the session's ID
	Type    string    `json:"type,omitempty"`    // the destination's type
	Target  string    `json:"target,omitempty"`  // the address with its secret parts hidden
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Skipped string    `json:"skipped,omitempty"` // why the event was not sent
	MS      int64     `json:"ms,omitempty"`
	Output  string    `json:"output,omitempty"` // a command's output, cut short
}
