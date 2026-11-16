package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// RootStepRecord is this laptop's identity-bound receipt of an attempted script.
// Confirmed means a fresh probe checked its effects. Printed is only a plan.
type RootStepRecord struct {
	HostID      string       `json:"host_id"`
	Steps       []RootStepID `json:"steps"`
	SHA256      string       `json:"sha256"`
	Mode        RootStepMode `json:"mode"`
	At          time.Time    `json:"at"`
	Status      string       `json:"status"`
	StartedSeen bool         `json:"started_seen,omitempty"`
}

type rootStepFile struct {
	Version    int              `json:"version"`
	Host       string           `json:"host"`
	Records    []RootStepRecord `json:"records"`
	Started    map[string]bool  `json:"started,omitempty"`
	root       map[string]json.RawMessage
	rawRecords []json.RawMessage
	future     bool
}

func validRootRecord(r RootStepRecord) bool {
	if !ValidHostID(r.HostID) || r.At.IsZero() || r.SHA256 != strings.ToLower(r.SHA256) {
		return false
	}
	sum, err := hex.DecodeString(r.SHA256)
	if err != nil || len(sum) != 32 {
		return false
	}
	if r.Mode != RootRun && r.Mode != RootPrint && r.Mode != RootSkip {
		return false
	}
	switch r.Status {
	case "running", "printed", "failed", "confirmed", "skipped":
	default:
		return false
	}
	seen := map[RootStepID]bool{}
	for _, id := range r.Steps {
		if _, ok := describeRootStep(id, "me", 1000); !ok || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func withRootStepState(ctx context.Context, h Host, write bool, change func(*rootStepFile) error) error {
	if !ValidHostName(h.Name) {
		return badHostNameError(h.Name)
	}
	return withStateFile(ctx, []string{"hosts", HostFileKey(h.Name)}, "root-steps.json", write,
		func(raw []byte, exists bool) ([]byte, error) {
			f := rootStepFile{Version: 1, Host: h.Name, Started: map[string]bool{}, root: map[string]json.RawMessage{}}
			if exists {
				if _, err := sandboxObjectContext(ctx, raw, "version", "host", "records", "started"); err != nil {
					return nil, badGrantStateError("root-steps.json")
				}
				if json.Unmarshal(raw, &f) != nil || f.Version != 1 || f.Host != h.Name || json.Unmarshal(raw, &f.root) != nil || f.root == nil {
					return nil, badGrantStateError("root-steps.json")
				}
				if data, ok := f.root["records"]; ok && json.Unmarshal(data, &f.rawRecords) != nil {
					return nil, badGrantStateError("root-steps.json")
				}
				for name := range f.root {
					if name != "version" && name != "host" && name != "records" && name != "started" {
						f.future = true
					}
				}
				knownRecord := []string{"host_id", "steps", "sha256", "mode", "at", "status", "started_seen"}
				for index, record := range f.Records {
					fields, err := sandboxObjectContext(ctx, f.rawRecords[index], knownRecord...)
					if err != nil || !validRootRecord(record) {
						return nil, badGrantStateError("root-steps.json")
					}
					for _, name := range knownRecord {
						delete(fields, name)
					}
					f.future = f.future || len(fields) != 0
				}
				if data := f.root["started"]; len(data) != 0 && string(data) != "null" {
					if _, err := sandboxObjectContext(ctx, data); err != nil {
						return nil, badGrantStateError("root-steps.json")
					}
				}
				for id, started := range f.Started {
					if !ValidHostID(id) || !started {
						return nil, badGrantStateError("root-steps.json")
					}
				}
			}
			if f.Started == nil {
				f.Started = map[string]bool{}
			}
			if err := change(&f); err != nil {
				return nil, err
			}
			if !write {
				return nil, nil
			}
			// Earlier records and unknown fields survive an append verbatim.
			for _, r := range f.Records[len(f.rawRecords):] {
				b, err := json.Marshal(r)
				if err != nil {
					return nil, fmt.Errorf("encode root-step record: %w", err)
				}
				f.rawRecords = append(f.rawRecords, b)
			}
			f.root["version"], _ = json.Marshal(f.Version)
			f.root["host"], _ = json.Marshal(f.Host)
			f.root["records"], _ = json.Marshal(f.rawRecords)
			f.root["started"], _ = json.Marshal(f.Started)
			out, err := json.MarshalIndent(f.root, "", "  ")
			if err != nil {
				return nil, fmt.Errorf("encode root steps: %w", err)
			}
			return append(out, '\n'), nil
		})
}

// RecordRootStep appends rather than replacing an earlier attempt. This keeps
// records of partial scripts and other host identities for later removal.
func RecordRootStep(ctx context.Context, h Host, r RootStepRecord) error {
	if !ValidHostID(r.HostID) {
		return rootStepError("the helper did not supply a host identity for the root-step record")
	}
	if !validRootRecord(r) {
		return rootStepError("the root-step record is incomplete")
	}
	if err := checkRootStepRecordPurpose(ctx, h, r); err != nil {
		return err
	}
	return withRootStepRecordObservation(ctx, h, r.HostID, func(owned context.Context) error {
		return withRootStepState(owned, h, true, func(f *rootStepFile) error {
			r.Steps = slices.Clone(r.Steps)
			f.Records = append(f.Records, r)
			return nil
		})
	})
}

// RootStepRecords reads the laptop's root-step history. An unsafe or malformed
// file is an error. It must never be treated as an empty history.
func RootStepRecords(ctx context.Context, h Host) ([]RootStepRecord, error) {
	var out []RootStepRecord
	err := withHostStateReadContext(ctx, h, func(owned context.Context) error {
		return withRootStepState(owned, h, false, func(f *rootStepFile) error { out = f.Records; return nil })
	})
	return out, err
}

// RootHistoryStarted reports the conservative local half of the default-mode
// decision. Unknown identities cannot be treated as a new, unused account.
func RootHistoryStarted(ctx context.Context, h Host, hostID string) (bool, error) {
	if !ValidHostID(hostID) {
		return true, nil
	}
	var sticky bool
	if err := withHostStateReadContext(ctx, h, func(owned context.Context) error {
		return withRootStepState(owned, h, false, func(f *rootStepFile) error {
			sticky = f.future || f.Started[hostID]
			for _, record := range f.Records {
				sticky = sticky || record.HostID == hostID && record.StartedSeen
			}
			return nil
		})
	}); err != nil {
		return false, err
	}
	if sticky {
		return true, nil
	}
	started, err := localSandboxHistoryStarted(ctx, h, hostID)
	if err != nil {
		return false, err
	}
	if started {
		return true, nil
	}
	return false, nil
}

// NoteSandboxStarted permanently marks an identity before a sandbox request
// can reach it. Failed or ambiguous starts keep the conservative print default.
// Removing a session or its policy cannot remove this safety history.
func NoteSandboxStarted(ctx context.Context, h Host, hostID string) error {
	if !ValidHostID(hostID) {
		return rootStepError("the sandbox has no host identity for its local history")
	}
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return err
	}
	if err := validateGrantRecordObservation(ctx, h, hostID); err != nil {
		return err
	}
	return withRootStepState(ctx, h, true, func(f *rootStepFile) error { f.Started[hostID] = true; return nil })
}

// WriteRootScript writes the original sealed print plan as an exclusive
// 0600 review file. Raw script/hash/host data cannot select a different UID
// or obtain a writer; the actual fresh probe retained by LOCALROOT supplies it.
func (c *Conn) WriteRootScript(ctx context.Context, rs RootScript) (string, error) {
	intent, err := requireRootIntent(ctx, c, rs, RootPrint)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalRootScript(intent.before, rs)
	if err != nil {
		return "", err
	}
	rs = canonical
	path := brand.Name + "-root-" + c.Host.Name + ".sh"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", &UserError{Msg: "cannot write root script " + path, Hint: "Move any existing file out of the way and run the command again.", Code: "root_script_file", Kind: KindFailed}
	}
	// A failed write retains its partial review file. The pathname may
	// have been replaced since O_EXCL; it grants no unlink authority.
	defer f.Close()
	failure := func(message string) error {
		return &UserError{Msg: message, Hint: "Inspect any partial review file, move it out of the way, and retry.", Code: "root_script_file", Kind: KindFailed}
	}
	if err := f.Chmod(0o600); err != nil {
		return "", failure("cannot set root script permissions")
	}
	if _, err := f.WriteString(rs.Text); err != nil {
		return "", failure("cannot write the root script")
	}
	if err := f.Sync(); err != nil {
		return "", failure("cannot sync the root script")
	}
	if err := f.Close(); err != nil {
		return "", failure("cannot close the root script")
	}
	return filepath.Join(".", path), nil
}
