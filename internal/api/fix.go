package api

// FixArgs asks the host doctor for its fixes. Without Apply it only lists
// them; with Apply it runs the ones named in IDs.
type FixArgs struct {
	Apply bool     `json:"apply,omitempty"`
	IDs   []string `json:"ids,omitempty"`
	// Terminfo holds terminfo sources from the laptop, by name, for the
	// terminfo fix.
	Terminfo map[string]string `json:"terminfo,omitempty"`
}

// Fix is one problem a doctor found, and what fixing it does.
type Fix struct {
	ID      string `json:"id"`
	Problem string `json:"problem"`
	// Action says in words what the fix does.
	Action string `json:"action"`
	// Command is the command the fix runs, for the user to see first.
	Command string `json:"command,omitempty"`
	// Manual is true when the doctor does not run the fix itself, for example
	// because it needs root: the user runs Command.
	Manual bool `json:"manual,omitempty"`
	// Done and Error report what happened when the fix ran.
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
	// Changed lists each change that the fix made, as it made it, such as
	// "removed <file>": also those of a fix that a cancel stopped before
	// its end. A fix of the host fills it in no version yet.
	Changed []string `json:"changed,omitempty"`
}
