package cachefs

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Reference is a durable native identity captured after canonical validation.
// It grants no authority by itself. Callers also bind its role, root generation
// and producer intent. Unknown future fields remain stored and never match.
type Reference struct {
	Version   int       `json:"version"`
	Identity  string    `json:"identity"`
	Mode      uint32    `json:"mode"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Directory bool      `json:"directory"`
	extra     map[string]json.RawMessage
}

// Known reports whether this implementation understands every witness field.
func (r Reference) Known() bool {
	return r.Version == 1 && r.Identity != "" && r.Mode&^uint32(0o777) == 0 && r.Size >= 0 && !r.Modified.IsZero() && len(r.extra) == 0
}

// CaptureFile records the validated anchored regular single-link descriptor.
func CaptureFile(root *os.Root, leaf string, file *os.File, mode fs.FileMode) (Reference, error) {
	info, err := ValidateFile(root, leaf, file, mode)
	if err != nil {
		return Reference{}, err
	}
	identity, err := referenceIdentity(file, info)
	if err != nil {
		return Reference{}, err
	}
	return Reference{Version: 1, Identity: identity, Mode: uint32(mode), Size: info.Size(), Modified: info.ModTime()}, nil
}

// CheckFile repeats all canonical native checks before comparing the witness.
func (r Reference) CheckFile(root *os.Root, leaf string, file *os.File, mode fs.FileMode) error {
	if !r.Known() || r.Directory || r.Mode != uint32(mode) {
		return fmt.Errorf("file reference is unknown or has another role mode")
	}
	fresh, err := CaptureFile(root, leaf, file, mode)
	if err != nil {
		return err
	}
	if r.Identity != fresh.Identity || r.Size != fresh.Size || !r.Modified.Equal(fresh.Modified) {
		return fmt.Errorf("file reference changed identity or contents metadata")
	}
	return nil
}

// CheckCreatedFile checks the same native object for cleanup of its creator's
// mutable uncommitted part. It repeats all current owner/ACL/link/mode checks
// but does not treat growing size, modification time or an authorised mode
// change as replacement. It is never sufficient for stable publication trust.
func (r Reference) CheckCreatedFile(root *os.Root, leaf string, file *os.File, mode fs.FileMode) error {
	if !r.Known() || r.Directory {
		return fmt.Errorf("creator file reference is unknown")
	}
	fresh, err := CaptureFile(root, leaf, file, mode)
	if err != nil {
		return err
	}
	if r.Identity != fresh.Identity {
		return fmt.Errorf("creator file reference changed native identity")
	}
	return nil
}

// CaptureRoot records an anchored owned directory using the same native
// owner/ACL/reparse policy as every cache root. It creates nothing.
func CaptureRoot(root *os.Root) (Reference, error) {
	if err := validateRoot(root, nil); err != nil {
		return Reference{}, err
	}
	file, err := root.Open(".")
	if err != nil {
		return Reference{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Reference{}, err
	}
	identity, err := referenceIdentity(file, info)
	if err != nil {
		return Reference{}, err
	}
	return Reference{Version: 1, Identity: identity, Mode: uint32(info.Mode().Perm()), Size: info.Size(), Modified: info.ModTime(), Directory: true}, nil
}

// CheckRoot checks native identity and mode. Directory entry changes are not
// generation changes, so directory size and modification time are not compared.
func (r Reference) CheckRoot(root *os.Root) error {
	if !r.Known() || !r.Directory {
		return fmt.Errorf("directory reference is unknown")
	}
	fresh, err := CaptureRoot(root)
	if err != nil {
		return err
	}
	if r.Identity != fresh.Identity || r.Mode != fresh.Mode {
		return fmt.Errorf("directory reference changed identity or mode")
	}
	return nil
}

// UnmarshalJSON retains future witness fields without treating them as known.
func (r *Reference) UnmarshalJSON(data []byte) error {
	type plain Reference
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"version", "identity", "mode", "size", "modified", "directory"} {
		delete(fields, name)
	}
	*r = Reference(value)
	r.extra = fields
	return nil
}

// MarshalJSON preserves future witness fields across metadata rewrites.
func (r Reference) MarshalJSON() ([]byte, error) {
	type plain Reference
	data, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for name, value := range r.extra {
		if _, exists := fields[name]; !exists {
			fields[name] = value
		}
	}
	return json.Marshal(fields)
}
