package gvisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
	"golang.org/x/sys/unix"
)

const nativeTopologyVersionV1 = 2

// NativeTopologyV1 is an owned immutable SETUP capsule. Its private constructor
// accepts the actual already-captured mount objects. It contains no guest
// link, secret, original OP, agent argv/environment, callback or raw OCI.
// Possession supplies setup data only; the original native boot owner and
// signed runtime closure independently authorise any actual execution.
type NativeTopologyV1 struct {
	mu          sync.Mutex
	descriptor  *os.File
	sources     []*os.File
	sourceIDs   []string
	wire        nativeTopologyWireV1
	digest      string
	closed      bool
	closeErr    error
	projections []*PublicTopologyV1
}

type nativeTopologyWireV1 struct {
	Version int                     `json:"v"`
	ID      string                  `json:"id"`
	Role    string                  `json:"role"`
	Mounts  []nativeTopologyMountV1 `json:"mounts"`
}

type nativeTopologyMountV1 struct {
	Role     string `json:"role"`
	Source   int    `json:"source"`
	Guest    string `json:"guest"`
	Write    bool   `json:"write"`
	Identity string `json:"identity"`
}

func freezeNativeTopologyV1(spec wall.Spec, plan []wall.Mount, sources []*os.File, smoke bool) (owned *NativeTopologyV1, failure error) {
	if !api.ValidSessionID(spec.ID) || spec.Image != nil || spec.VM != nil || spec.Root != wall.Reset || len(plan) != len(sources) || len(sources) > 1000 {
		return nil, topologyFailureV1("the actual setup projection has another original purpose")
	}
	// Reuse the existing closed guest-path/private-filesystem barrier, without
	// serialising its process/root/path OCI object as executable input.
	if err := (files.StatePlan{Dirs: spec.StateDirs, Files: spec.StateFiles}).Validate(); err != nil {
		return nil, err
	}
	if _, err := makeOCI(spec, plan, smoke); err != nil {
		return nil, err
	}
	wire := nativeTopologyWireV1{Version: nativeTopologyVersionV1, ID: spec.ID, Role: "guest-init", Mounts: []nativeTopologyMountV1{}}
	if smoke {
		wire.Role = "probe-true"
	}
	result := &NativeTopologyV1{wire: wire}
	complete := false
	defer func() {
		if !complete {
			owned = result
			failure = errors.Join(failure, result.Close())
		}
	}()
	for index, mount := range plan {
		source := sources[index]
		if source == nil || mount.Identity == "" {
			return nil, topologyFailureV1("a setup mount lost its actual captured object")
		}
		role, err := nativeTopologyMountRoleV2(spec, mount)
		if err != nil {
			return nil, err
		}
		if role != "mount" {
			info, err := source.Stat()
			if err != nil || !info.IsDir() {
				return nil, errors.Join(err, topologyFailureV1("kept state is not its original native directory object"))
			}
		}
		identity, err := files.Identity(source)
		if err != nil || identity != mount.Identity {
			return nil, errors.Join(err, topologyFailureV1("the setup object differs from its original captured identity"))
		}
		fd, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return nil, err
		}
		held := os.NewFile(uintptr(fd), "gvisor-native-topology-source")
		result.sources = append(result.sources, held)
		current, err := files.Identity(held)
		if err != nil || current != identity {
			return nil, errors.Join(err, topologyFailureV1("the actual captured setup source changed during retention"))
		}
		result.sourceIDs = append(result.sourceIDs, identity)
		result.wire.Mounts = append(result.wire.Mounts, nativeTopologyMountV1{Role: role, Source: index, Guest: mount.Guest, Write: mount.Write, Identity: identity})
	}
	raw, err := json.Marshal(result.wire)
	if err != nil || len(raw) > proto.MaxFrame {
		return nil, errors.Join(err, topologyFailureV1("the immutable setup descriptor exceeds its existing frame bound"))
	}
	descriptor, err := sealNativeTopologyV1(raw)
	result.descriptor = descriptor
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	result.digest = hex.EncodeToString(digest[:])
	complete = true
	return result, nil
}

func sealNativeTopologyV1(raw []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("native-topology-v1", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "gvisor-immutable-setup")
	fail := func(cause error) (*os.File, error) { return file, cause }
	if n, err := file.Write(raw); err != nil || n != len(raw) {
		return fail(errors.Join(err, io.ErrShortWrite))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	required := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, required); err != nil {
		return fail(err)
	}
	got, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || got&required != required {
		return fail(errors.Join(err, topologyFailureV1("the actual setup descriptor is not immutable")))
	}
	return file, nil
}

// checkNativeTopologyWireV1 is a strict data barrier, never a boot grant.
// All source indices name supplied native objects, not configured host paths.
func checkNativeTopologyWireV1(raw []byte, sourceCount int) (nativeTopologyWireV1, error) {
	var wire nativeTopologyWireV1
	if len(raw) > proto.MaxFrame || sourceCount < 0 || sourceCount > 1000 || !api.ExactJSONFields(raw, wire) {
		return wire, topologyFailureV1("the setup descriptor has unsupported fields or shape")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return wire, err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return wire, topologyFailureV1("the setup descriptor has trailing input")
	}
	var rawShape struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if json.Unmarshal(raw, &rawShape) != nil {
		return wire, topologyFailureV1("the native mount shape is unsupported")
	}
	for _, record := range rawShape.Mounts {
		fields, err := api.JSONObjectFields(record, "role", "source", "guest", "write", "identity")
		if err != nil || len(fields) != 5 {
			return wire, topologyFailureV1("native mounts need their exact closed role and complete fields")
		}
		for _, field := range fields {
			if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return wire, topologyFailureV1("a native mount field is null")
			}
		}
	}
	if wire.Version != nativeTopologyVersionV1 || !api.ValidSessionID(wire.ID) || wire.Role != "guest-init" && wire.Role != "probe-true" || len(wire.Mounts) != sourceCount {
		return wire, topologyFailureV1("the setup descriptor is not its closed versioned role")
	}
	seen := map[string]bool{}
	for index, mount := range wire.Mounts {
		if mount.Source != index || mount.Identity == "" || !path.IsAbs(mount.Guest) || path.Clean(mount.Guest) != mount.Guest || mount.Guest == "/" || mount.Guest == "/home" || strings.ContainsAny(mount.Guest, "\x00\r\n") || seen[mount.Guest] {
			return wire, topologyFailureV1("the setup descriptor changed a native source or guest target")
		}
		seen[mount.Guest] = true
		if err := checkNativeTopologyMountRoleV2(mount); err != nil {
			return wire, err
		}
		for _, reserved := range []string{"/proc", "/dev", "/tmp", "/run"} {
			if mount.Guest == reserved || strings.HasPrefix(mount.Guest, reserved+"/") {
				return wire, topologyFailureV1("setup overlaps the fixed private guest topology")
			}
		}
	}
	return wire, nil
}

func (topology *NativeTopologyV1) checkLocked() error {
	if topology == nil || topology.closed || topology.descriptor == nil || topology.digest == "" || len(topology.sources) != len(topology.sourceIDs) {
		return topologyFailureV1("the original immutable setup capsule ended")
	}
	required := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	seals, err := unix.FcntlInt(topology.descriptor.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&required != required {
		return errors.Join(err, topologyFailureV1("the setup object's native seal changed"))
	}
	for index, source := range topology.sources {
		identity, err := files.Identity(source)
		if err != nil || identity != topology.sourceIDs[index] {
			return errors.Join(err, topologyFailureV1("an actual retained topology object changed"))
		}
	}
	return nil
}

// Close releases this capsule's original descriptor and captured source loans.
func (topology *NativeTopologyV1) Close() error {
	if topology == nil {
		return nil
	}
	topology.mu.Lock()
	defer topology.mu.Unlock()
	if topology.closeErr != nil {
		return topology.closeErr
	}
	if topology.closed {
		return nil
	}
	topology.closed = true
	for _, projection := range topology.projections {
		if err := projection.closeParentOwnedV1(); err != nil {
			topology.closeErr = errors.Join(topology.closeErr, err)
		}
	}
	if topology.closeErr != nil {
		return topology.closeErr
	}
	if topology.descriptor != nil {
		if err := topology.descriptor.Close(); err != nil {
			topology.closeErr = errors.Join(topology.closeErr, err)
		} else {
			topology.descriptor = nil
		}
	}
	for index, source := range topology.sources {
		if source == nil {
			continue
		}
		if err := source.Close(); err != nil {
			topology.closeErr = errors.Join(topology.closeErr, err)
		} else {
			topology.sources[index] = nil
		}
	}
	if topology.closeErr != nil {
		return topology.closeErr
	}
	topology.sources, topology.sourceIDs = nil, nil
	return nil
}

func topologyFailureV1(message string) error {
	return failure("wall_setup", "the closed native topology refused: "+message, nil)
}

// CheckSetupV1 validates the SAME immutable original capsule and native mount
// identities against the original host factory projection. It grants no boot.
func (topology *NativeTopologyV1) CheckSetupV1(spec wall.Spec, plan []wall.Mount) error {
	if topology == nil {
		return topologyFailureV1("setup has no original native capsule")
	}
	topology.mu.Lock()
	defer topology.mu.Unlock()
	if err := topology.checkLocked(); err != nil {
		return err
	}
	if topology.wire.ID != spec.ID || len(topology.wire.Mounts) != len(plan) {
		return topologyFailureV1("the capsule belongs to another original setup")
	}
	for index, mount := range plan {
		entry := topology.wire.Mounts[index]
		role, err := nativeTopologyMountRoleV2(spec, mount)
		if err != nil {
			return err
		}
		if entry.Role != role {
			return topologyFailureV1("the original mount object changed its closed setup role")
		}
		if entry.Source != index || entry.Guest != mount.Guest || entry.Write != mount.Write || entry.Identity != mount.Identity {
			return topologyFailureV1("the original setup projection changed after capture")
		}
	}
	data, err := io.ReadAll(io.NewSectionReader(topology.descriptor, 0, proto.MaxFrame+1))
	if err != nil || len(data) > proto.MaxFrame {
		return errors.Join(err, topologyFailureV1("the held setup descriptor became unreadable"))
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != topology.digest {
		return topologyFailureV1("the actual immutable setup bytes differ from their original capture")
	}
	if _, err := checkNativeTopologyWireV1(data, len(topology.sources)); err != nil {
		return err
	}
	return nil
}

// A role labels only objects selected by the SAME original process MountPlan.
// Existing approved state is opaque filesystem input, not a new host-home or
// injected secret/agent command. No state file contents are parsed in setup.
func nativeTopologyMountRoleV2(spec wall.Spec, mount wall.Mount) (string, error) {
	for _, state := range spec.StateDirs {
		if mount.Guest == "/home/agent/"+state {
			expected := filepath.Join(spec.SessionDir, "state", filepath.FromSlash(state))
			if mount.Host != expected || !mount.Write {
				return "", topologyFailureV1("kept state is not its actual original per-session native object")
			}
			return "kept-dir", nil
		}
	}
	if len(spec.StateFiles) > 0 {
		if mount.Guest == files.RestorePath {
			if mount.Host != spec.StateRestore || mount.Write {
				return "", topologyFailureV1("restore state differs from its original captured generation")
			}
			return "state-restore", nil
		}
		if mount.Guest == files.CapturePath {
			if mount.Host != spec.StateCapture || !mount.Write {
				return "", topologyFailureV1("capture state differs from its original captured generation")
			}
			return "state-capture", nil
		}
	}
	if mount.Guest == "/home" || strings.HasPrefix(mount.Guest, "/home/") || mount.Guest == files.RestorePath || mount.Guest == files.CapturePath {
		return "", topologyFailureV1("setup cannot invent a home or kept-state grant")
	}
	return "mount", nil
}

func checkNativeTopologyMountRoleV2(mount nativeTopologyMountV1) error {
	switch mount.Role {
	case "mount":
		if mount.Guest == "/home" || strings.HasPrefix(mount.Guest, "/home/") || mount.Guest == files.RestorePath || mount.Guest == files.CapturePath {
			return topologyFailureV1("an ordinary mount claimed private home or state topology")
		}
	case "kept-dir":
		relative, ok := strings.CutPrefix(mount.Guest, "/home/agent/")
		if !ok || !mount.Write {
			return topologyFailureV1("kept state has another guest target or access mode")
		}
		if err := (files.StatePlan{Dirs: []string{relative}}).Validate(); err != nil {
			return err
		}
	case "state-restore":
		if mount.Guest != files.RestorePath || mount.Write {
			return topologyFailureV1("restore state has another fixed target or access mode")
		}
	case "state-capture":
		if mount.Guest != files.CapturePath || !mount.Write {
			return topologyFailureV1("capture state has another fixed target or access mode")
		}
	default:
		return topologyFailureV1("the native mount has an unsupported setup role")
	}
	return nil
}

// PublicTopologyV1 is a public, owned native-object projection. It carries no
// private input or launch authority; the original topology and native boot
// owner remain independently required by the mapped controller producer.
type PublicTopologyV1 struct {
	parentOwned bool
	mu          sync.Mutex
	closeErr    error
	ID, Role    string
	Descriptor  *os.File
	Sources     []*os.File
}

// Close releases only this public projection's owned descriptors.
func (projection *PublicTopologyV1) Close() error {
	if projection == nil {
		return nil
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	return projection.closeLockedV1()
}
func (projection *PublicTopologyV1) closeLockedV1() error {
	if projection.closeErr != nil {
		return projection.closeErr
	}
	if projection.Descriptor != nil {
		if err := projection.Descriptor.Close(); err != nil {
			projection.parentOwned = true
			projection.closeErr = errors.Join(projection.closeErr, err)
		} else {
			projection.Descriptor = nil
		}
	}
	for index, file := range projection.Sources {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil {
			projection.parentOwned = true
			projection.closeErr = errors.Join(projection.closeErr, err)
		} else {
			projection.Sources[index] = nil
		}
	}
	if projection.closeErr == nil {
		projection.Sources = nil
	}
	return projection.closeErr
}

// RetainPublicV1 duplicates this same immutable capsule and its native sources.
func (projection *PublicTopologyV1) closeParentOwnedV1() error {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if !projection.parentOwned {
		return nil
	}
	return projection.closeLockedV1()
}

func (topology *NativeTopologyV1) RetainPublicV1() (_ *PublicTopologyV1, failure error) {
	if topology == nil {
		return nil, topologyFailureV1("the original public topology is absent")
	}
	topology.mu.Lock()
	defer topology.mu.Unlock()
	if err := topology.checkLocked(); err != nil {
		return nil, err
	}
	projection := &PublicTopologyV1{ID: topology.wire.ID, Role: topology.wire.Role}
	topology.projections = append(topology.projections, projection)
	complete := false
	defer func() {
		if !complete {
			projection.mu.Lock()
			projection.parentOwned = true
			projection.mu.Unlock()
			failure = errors.Join(failure, projection.Close())
		}
	}()
	fd, err := unix.FcntlInt(topology.descriptor.Fd(), unix.F_DUPFD_CLOEXEC, 32)
	if err != nil {
		return nil, err
	}
	projection.Descriptor = os.NewFile(uintptr(fd), "same-original-public-controller-topology")
	for _, source := range topology.sources {
		fd, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 32)
		if err != nil {
			return nil, err
		}
		projection.Sources = append(projection.Sources, os.NewFile(uintptr(fd), "same-original-public-controller-source"))
	}
	complete = true
	return projection, nil
}
