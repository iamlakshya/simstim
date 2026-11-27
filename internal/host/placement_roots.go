package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

type persistentRootBinding struct {
	State  string `json:"state"`
	Data   string `json:"data"`
	Config string `json:"config"`
	extra  map[string]json.RawMessage
}
type runtimeRootReceipt struct {
	Path    string   `json:"path"`
	Ref     purgeRef `json:"ref"`
	Created bool     `json:"created"`
	extra   map[string]json.RawMessage
}

func currentPersistentRoots() persistentRootBinding {
	return persistentRootBinding{State: paths.StateDir(), Data: paths.DataDir(), Config: paths.ConfigDir()}
}
func (root persistentRootBinding) known() bool {
	if len(root.extra) != 0 {
		return false
	}
	for _, path := range []string{root.State, root.Data, root.Config} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != brand.Name {
			return false
		}
	}
	return true
}
func checkPersistentRoots(control placementControl) error {
	bound, current := control.PersistentRoots, currentPersistentRoots()
	if control.ScopeFormat != 2 || !bound.known() {
		return placementError("the existing guard has an unknown or unsupported recorded scope format")
	}
	if bound.State != current.State || bound.Data != current.Data || bound.Config != current.Config {
		return UserError("root_configuration", "this home already has an active host domain with different recorded XDG roots", "Restore the recorded state, data and config roots or finish that domain's removal before changing them.")
	}
	return nil
}

func runtimeRootKey(path string) string { return footprintDigest([]byte(path)) }
func validRuntimeRootPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && (filepath.Base(path) == brand.Name || filepath.Base(path) == brand.Name+"-"+strconv.Itoa(os.Getuid()))
}
func initialRuntimeRoots() (map[string]runtimeRootReceipt, error) {
	path := paths.RuntimeDir()
	if !validRuntimeRootPath(path) {
		return nil, placementError("the runtime root has no closed absolute role")
	}
	receipt := runtimeRootReceipt{Path: path}
	root, err := cachefs.OpenPrivateRoot(path, false)
	if err == nil {
		dir, openErr := root.Open(".")
		if openErr == nil {
			info, statErr := dir.Stat()
			dir.Close()
			if statErr == nil {
				receipt.Ref = refOf(info)
			}
		}
		root.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		// An unreadable legacy runtime path grants no cleanup ownership.
		receipt.Ref = purgeRef{}
	}
	return map[string]runtimeRootReceipt{runtimeRootKey(path): receipt}, nil
}

func (binding *persistentRootBinding) UnmarshalJSON(data []byte) error {
	type plain persistentRootBinding
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"state", "data", "config"})
	*binding = persistentRootBinding(value)
	binding.extra = extra
	return err
}
func (binding persistentRootBinding) MarshalJSON() ([]byte, error) {
	type plain persistentRootBinding
	return encodeFootprint(plain(binding), binding.extra)
}
func (receipt *runtimeRootReceipt) UnmarshalJSON(data []byte) error {
	type plain runtimeRootReceipt
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"path", "ref", "created"})
	*receipt = runtimeRootReceipt(value)
	receipt.extra = extra
	return err
}
func (receipt runtimeRootReceipt) MarshalJSON() ([]byte, error) {
	type plain runtimeRootReceipt
	return encodeFootprint(plain(receipt), receipt.extra)
}

func samePersistentRootBinding(a, b persistentRootBinding) bool {
	return a.known() && b.known() && a.State == b.State && a.Data == b.Data && a.Config == b.Config
}
