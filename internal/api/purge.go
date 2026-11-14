package api

import "encoding/json"

// PurgeArgs names one client-authorised removal attempt. The host verifies its
// own identity before any mutation. Force bypasses work-loss checks only.
type PurgeArgs struct {
	HostID string `json:"expected_host_id"`
	Nonce  string `json:"operation_nonce"`
	Force  bool   `json:"force,omitempty"`
}

// PurgeItem reports a fixed resource and its actual outcome. A path in a report
// gives the client no authority to delete a file or execute a command.
type PurgeItem struct {
	Kind   string `json:"kind"`
	Count  uint64 `json:"count,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
	Hint   string `json:"hint,omitempty"`
	extra  map[string]json.RawMessage
}

// PurgeSession identifies work that must be saved or explicitly discarded.
type PurgeSession struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
	extra  map[string]json.RawMessage
}

// PurgeResult is the helper's removal report. Removed and Left distinguish
// completed cleanup from foreign, modified, unknown and failed resources.
// HelperGone is an observed cleanup result, never permission to reinstall it.
type PurgeResult struct {
	HostID       string             `json:"host_id"`
	Scope        string             `json:"scope"`
	User         string             `json:"user"`
	UID          int                `json:"uid"`
	Arch         string             `json:"arch"`
	HostComplete bool               `json:"host_complete"`
	Nonce        string             `json:"operation_nonce"`
	Done         []string           `json:"done"`
	Removed      []PurgeItem        `json:"removed"`
	Left         []PurgeItem        `json:"left"`
	Unsaved      []PurgeSession     `json:"unsaved,omitempty"`
	Logins       []string           `json:"logins,omitempty"`
	GuardWitness *PurgeGuardWitness `json:"guard_witness,omitempty"`
	GuardRemoved bool               `json:"guard_removed"`
	HelperGone   bool               `json:"helper_gone"`
	extra        map[string]json.RawMessage
}
