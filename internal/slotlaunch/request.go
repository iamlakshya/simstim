// Package slotlaunch owns the optional root-owned sandbox launcher. Requests
// carry bounded data; they never contain a command, host path or user identity.
package slotlaunch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

const RequestMax = 64 << 10
const SlotCount = 16
const artifactMax int64 = 1 << 40

// Request is the closed, versionless wire format of isolation spec 21.9.
// Parse checks exact, operation-specific field sets before any side effect.
type Request struct {
	Op              string   `json:"op"`
	Session         string   `json:"session,omitempty"`
	Driver          string   `json:"driver,omitempty"`
	SHA256          string   `json:"sha256,omitempty"`
	Size            int64    `json:"size,omitempty"`
	MetaSize        int64    `json:"meta_size,omitempty"`
	SigSize         int64    `json:"sig_size,omitempty"`
	VMM             string   `json:"vmm,omitempty"`
	Kernel          string   `json:"kernel,omitempty"`
	Tools           string   `json:"tools,omitempty"`
	Image           string   `json:"image,omitempty"`
	Agents          string   `json:"agents,omitempty"`
	ToolsRootHash   string   `json:"tools_root_hash,omitempty"`
	ToolsDataBlocks int64    `json:"tools_data_blocks,omitempty"`
	ImageRootHash   string   `json:"image_root_hash,omitempty"`
	ImageDataBlocks int64    `json:"image_data_blocks,omitempty"`
	CPUs            int      `json:"cpus,omitempty"`
	MemoryMiB       int64    `json:"memory_mib,omitempty"`
	DiskMaxGiB      int64    `json:"disk_max_gib,omitempty"`
	From            string   `json:"from,omitempty"`
	To              string   `json:"to,omitempty"`
	Keep            []string `json:"keep,omitempty"`
	Action          string   `json:"action,omitempty"`
	Name            string   `json:"name,omitempty"`
	Generation      string   `json:"generation,omitempty"`
	KeepCount       int      `json:"keep_count,omitempty"`
	Needed          int64    `json:"needed,omitempty"`
	Safety          string   `json:"safety,omitempty"`
}

var fields = map[string][]string{
	"update":            {"op", "meta_size", "sig_size", "size"},
	"store":             {"op", "sha256", "size"},
	"start":             {"op", "session", "driver", "vmm", "kernel", "tools", "image", "agents", "tools_root_hash", "tools_data_blocks", "image_root_hash", "image_data_blocks", "cpus", "memory_mib", "disk_max_gib"},
	"start.fork":        {"op", "session", "driver", "vmm", "kernel", "tools", "image", "agents", "tools_root_hash", "tools_data_blocks", "image_root_hash", "image_data_blocks", "cpus", "memory_mib", "disk_max_gib"},
	"start.reserved-v2": {"op", "session", "driver", "vmm", "kernel", "tools", "image", "agents", "tools_root_hash", "tools_data_blocks", "image_root_hash", "image_data_blocks", "cpus", "memory_mib", "disk_max_gib"},
	"kill":              {"op", "session"}, "destroy": {"op", "session"}, "native": {"op", "session"},
	"copy": {"op", "from", "to"}, "prune": {"op", "keep"}, "slots": {"op"},
	"checkpoint":             {"op", "session", "action", "name", "generation", "keep_count", "meta_size"},
	"checkpoint.read":        {"op", "session", "action", "name", "generation", "keep_count", "meta_size"},
	"checkpoint.discard-v2":  {"op", "session", "action", "name", "generation", "keep_count", "meta_size"},
	"checkpoint.space":       {"op", "session", "needed"},
	"checkpoint.reserve-v2":  {"op", "session"},
	"checkpoint.stop-v2":     {"op", "session"},
	"reserved.cleanup-v2":    {"op", "session"},
	"reserved.destroy-v2":    {"op", "session"},
	"reserved.abandon-v2":    {"op", "session"},
	"checkpoint.stopped-v2":  {"op", "session", "action", "name", "generation", "keep_count", "meta_size"},
	"checkpoint.prepare-v2":  {"op", "session", "name", "generation", "safety", "meta_size"},
	"checkpoint.exchange-v2": {"op", "session", "name", "generation", "safety", "meta_size"},
	"checkpoint.rollback-v2": {"op", "session", "name", "generation", "safety", "meta_size"},
	"checkpoint.finalize-v2": {"op", "session", "name", "generation", "safety", "meta_size"},
}

type launchError struct{ Code, Message, Hint string }

func (e *launchError) Error() string { return e.Message + "\nTry: " + e.Hint }

// Unwrap preserves the structured native refusal through ordinary error
// wrappers. A slot's actual no_room result must not become sandbox_boot.
func (e *launchError) Unwrap() error {
	return &proto.Error{Code: e.Code, Message: e.Message, Hint: e.Hint}
}
func refused(message string) error {
	return &launchError{Code: "launcher_refused", Message: message, Hint: "Run the host doctor and check its separate-account installation."}
}

// ReadRequest consumes exactly one bounded JSON line. Any following bytes
// remain in the returned reader for the declared binary payload.
func ReadRequest(in io.Reader) (Request, *bufio.Reader, error) {
	r := bufio.NewReaderSize(in, RequestMax+1)
	line, err := r.ReadSlice('\n')
	if err != nil || len(line) > RequestMax {
		return Request{}, r, refused("the launcher request is not one JSON line of at most 64 KiB")
	}
	request, err := Parse(line)
	return request, r, err
}

// Parse rejects duplicate, aliased, missing and unknown fields, including a
// valid field belonging to another operation. It performs no filesystem I/O.
func Parse(raw []byte) (Request, error) {
	var out Request
	if len(raw) == 0 || len(raw) > RequestMax {
		return out, refused("the launcher request exceeds its bound")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return out, refused("the launcher request is not a JSON object")
	}
	seen := map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return out, refused("the launcher request has an invalid field")
		}
		if _, duplicate := seen[name]; duplicate {
			return out, refused("the launcher request repeats a field")
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return out, refused("the launcher request has an invalid value")
		}
		seen[name] = value
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return out, refused("the launcher request is incomplete")
	}
	if _, err = dec.Token(); err != io.EOF {
		return out, refused("the launcher request contains trailing JSON")
	}
	if json.Unmarshal(seen["op"], &out.Op) != nil {
		return out, refused("the launcher request has no operation")
	}
	allowed, ok := fields[out.Op]
	if !ok || len(allowed) != len(seen) {
		return Request{}, refused("the launcher operation or its fields are unknown")
	}
	for _, key := range allowed {
		if _, ok := seen[key]; !ok || bytes.Equal(bytes.TrimSpace(seen[key]), []byte("null")) {
			return Request{}, refused("the launcher request has a missing or unknown field")
		}
	}
	if json.Unmarshal(raw, &out) != nil {
		return Request{}, refused("the launcher request has an invalid field type")
	}
	if err := out.validate(); err != nil {
		return Request{}, err
	}
	return out, nil
}

func (r Request) validate() error {
	switch r.Op {
	case "store":
		if !supply.IsHex64(r.SHA256) || r.Size < 1 || r.Size > artifactMax {
			return refused("the store request has an invalid digest or size")
		}
	case "update":
		if r.MetaSize < 1 || r.MetaSize > RequestMax || r.SigSize < 1 || r.SigSize > RequestMax || r.Size < 1 || r.Size > 256<<20 {
			return refused("the update payload has an invalid size")
		}
	case "start", "start.fork", "start.reserved-v2":
		if !api.ValidSessionID(r.Session) || r.Driver != "chv" && r.Driver != "firecracker" && r.Driver != "nitro" {
			return refused("the start request has an invalid session or driver")
		}
		for _, digest := range r.digests() {
			if !supply.IsHex64(digest) {
				return refused("the start request has an invalid store digest")
			}
		}
		if !supply.IsHex64(r.ToolsRootHash) || !supply.IsHex64(r.ImageRootHash) || r.ToolsDataBlocks <= 0 || r.ImageDataBlocks <= 0 || r.ToolsDataBlocks > math.MaxInt64/4096 || r.ImageDataBlocks > math.MaxInt64/4096 {
			return refused("the start request has invalid verity bounds")
		}
		if r.Driver == "nitro" && r.MemoryMiB%2 != 0 {
			return refused("Nitro guest memory must be a multiple of 2 MiB")
		}
		maxCPU := 8192
		if r.Driver == "nitro" {
			maxCPU = 1024
		}
		if r.Driver == "firecracker" {
			maxCPU = 32
		}
		if r.CPUs < 1 || r.CPUs > maxCPU || r.MemoryMiB < 128 || r.MemoryMiB > 1<<30 || r.DiskMaxGiB < 1 || r.DiskMaxGiB > math.MaxInt64/(1<<30) {
			return refused("the start request has invalid resource limits")
		}
	case "kill", "destroy", "native", "checkpoint.reserve-v2", "checkpoint.stop-v2", "reserved.cleanup-v2", "reserved.destroy-v2", "reserved.abandon-v2":
		if !api.ValidSessionID(r.Session) {
			return refused("the request has an invalid session ID")
		}
	case "copy":
		if _, _, ok := diskName(r.From); !ok {
			return refused("the copy source is not a session disk or checkpoint")
		}
		if _, _, ok := diskName(r.To); !ok || r.From == r.To {
			return refused("the copy destination is invalid or aliases its source")
		}
	case "checkpoint", "checkpoint.read", "checkpoint.discard-v2", "checkpoint.stopped-v2":
		if !api.ValidSessionID(r.Session) || r.KeepCount < 0 || r.KeepCount > 1024 || r.MetaSize < 0 || r.MetaSize > 64<<10 {
			return refused("the checkpoint operation has invalid bounded data")
		}
		if r.Op == "checkpoint.read" || r.Op == "checkpoint.discard-v2" {
			when, err := time.Parse("20060102T150405.000Z", r.Name)
			wanted := "check"
			if r.Op == "checkpoint.discard-v2" {
				wanted = "discard"
			}
			if r.Action != wanted || err != nil || when.Format("20060102T150405.000Z") != r.Name || r.Generation != "" || r.KeepCount != 0 || r.MetaSize != 0 {
				return refused("the read-only checkpoint check has invalid fixed roles")
			}
			return nil
		}
		switch r.Action {
		case "open", "probe", "list", "trim", "space":
			if r.Name != "" || r.Generation != "" || r.MetaSize != 0 || r.Action != "trim" && r.KeepCount != 0 {
				return refused("the checkpoint operation has unrelated fields")
			}
		case "create", "select":
			when, err := time.Parse("20060102T150405.000Z", r.Name)
			if err != nil || when.Format("20060102T150405.000Z") != r.Name || r.Generation != "" || r.Action == "create" && r.MetaSize == 0 || r.Action == "select" && (r.MetaSize != 0 || r.KeepCount != 0) {
				return refused("the checkpoint operation has an invalid fixed timestamp role")
			}
		default:
			return refused("the checkpoint action is unknown")
		}
	case "checkpoint.space":
		if !api.ValidSessionID(r.Session) || r.Needed < 1 {
			return refused("the checkpoint space check has invalid source size data")
		}
	case "checkpoint.prepare-v2", "checkpoint.exchange-v2", "checkpoint.rollback-v2", "checkpoint.finalize-v2":
		if !api.ValidSessionID(r.Session) || len(r.Generation) != 32 || r.MetaSize < 0 || r.MetaSize > 64<<10 {
			return refused("the reserved replacement request has invalid bounded data")
		}
		for _, value := range r.Generation {
			if value < '0' || value > '9' && value < 'a' || value > 'f' {
				return refused("the replacement generation is not lowercase hexadecimal")
			}
		}
		for _, name := range []string{r.Name, r.Safety} {
			when, err := time.Parse("20060102T150405.000Z", name)
			if err != nil || when.Format("20060102T150405.000Z") != name {
				return refused("a reserved replacement checkpoint role is invalid")
			}
		}
		if r.Op == "checkpoint.prepare-v2" && r.MetaSize == 0 {
			return refused("the replacement safety metadata is absent")
		}
	case "prune":
		if len(r.Keep) > 1024 {
			return refused("the prune keep list exceeds its bound")
		}
		seen := map[string]bool{}
		for _, digest := range r.Keep {
			if !supply.IsHex64(digest) || seen[digest] {
				return refused("the prune keep list has an invalid or repeated digest")
			}
			seen[digest] = true
		}
	case "slots":
	default:
		return refused("the launcher operation is unknown")
	}
	return nil
}

func (r Request) digests() []string { return []string{r.VMM, r.Kernel, r.Tools, r.Image, r.Agents} }

func diskName(value string) (session, relative string, ok bool) {
	parts := strings.Split(value, "/")
	if !api.ValidSessionID(parts[0]) {
		return "", "", false
	}
	if len(parts) == 1 {
		return parts[0], "disk.img", true
	}
	if len(parts) != 3 || parts[1] != "checkpoints" {
		return "", "", false
	}
	// Spec 21.2 uses the UTC RFC3339 basic form, with no path punctuation.
	const layout = "20060102T150405.000Z"
	when, err := time.Parse(layout, parts[2])
	if err != nil || when.Format(layout) != parts[2] {
		return "", "", false
	}
	return parts[0], "checkpoints/" + parts[2] + ".disk", true
}
