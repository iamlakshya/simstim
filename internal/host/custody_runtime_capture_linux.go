//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

// captureCustodyRuntimeV1 captures configuration only from the actual held
// manager's canonical live or retained service. These handles grant no boot,
// stopped state or cold authority; their only mutation role is exact cleanup.
func (manager *nativeUserManagerV1) captureCustodyRuntimeV1(ctx context.Context, path dbus.ObjectPath, id string) (_ *custodyRuntimeV1, failure error) {
	enabled, err := manager.custodyPropertiesV1(ctx, path)
	if err != nil || !enabled {
		return nil, err
	}
	fragment, err := manager.getProperty(ctx, path, systemdUnitInterface, "FragmentPath")
	if err != nil {
		return nil, err
	}
	if actual, ok := fragment.Value().(string); !ok || actual != filepath.Join(manager.rootPath, "systemd", "user", UnitName(id)) {
		return nil, sessionLeaseError("the custody service has no exact owned runtime fragment")
	}
	config, err := manager.lockCustodyConfigV1(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { failure = errors.Join(failure, config.Close()) }()
	runtime := &custodyRuntimeV1{}
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, runtime.Close())
		}
	}()
	runtime.root, err = config.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	runtime.service, err = captureCustodyConfigFileV1(runtime.root, UnitName(id))
	if err != nil {
		return nil, err
	}
	if err := validateCustodyServiceFileV1(runtime.service.content); err != nil {
		return nil, err
	}
	runtime.pins, err = cachefs.OpenPrivateChild(config.root, custodyTargetV1()+".d", false)
	if err != nil {
		return nil, err
	}
	name, content, err := custodyPinFileV1(id)
	if err != nil {
		return nil, err
	}
	runtime.pin, err = openCustodyConfigFileV1(runtime.pins, name, content, false)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(runtime.service.check(), runtime.pin.check(), manager.checkCustodyPinV1(ctx, UnitName(id)), config.check(ctx)); err != nil {
		return nil, err
	}
	complete = true
	return runtime, nil
}

func captureCustodyConfigFileV1(root *os.Root, name string) (_ *custodyConfigFileV1, failure error) {
	file, err := cachefs.OpenFile(root, name, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			failure = errors.Join(failure, file.Close())
		}
	}()
	content, err := io.ReadAll(io.LimitReader(file, (256<<10)+1))
	if err != nil || len(content) > 256<<10 {
		return nil, errors.Join(sessionLeaseError("the owned runtime custody fragment exceeds its closed bound"), err)
	}
	ref, err := cachefs.CaptureFile(root, name, file, 0o600)
	if err != nil {
		return nil, err
	}
	result := &custodyConfigFileV1{root: root, name: name, file: file, ref: ref, content: content}
	if err := result.check(); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}

func validateCustodyServiceFileV1(content []byte) error {
	if !strings.HasPrefix(string(content), "# Owned session custody v1\n[Unit]\n") || strings.ContainsRune(string(content), 0) {
		return sessionLeaseError("the retained service fragment is not an owned closed custody unit")
	}
	allowed := map[string]bool{"Description": true, "CollectMode": true, "Type": true, "Slice": true, "KillMode": true, "TimeoutStopSec": true, "OOMPolicy": true, "Restart": true, "TasksMax": true, "NotifyAccess": true, "FileDescriptorStoreMax": true, "FileDescriptorStorePreserve": true, "ExecStart": true, "Environment": true, "UnsetEnvironment": true, "MemoryMax": true, "MemorySwapMax": true}
	seen := make(map[string]string)
	section := ""
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "[Unit]" || line == "[Service]" {
			section = line
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !allowed[key] || section == "" || seen[key] != "" && key != "Environment" {
			return sessionLeaseError("the retained runtime service has an unknown or repeated directive")
		}
		if section == "[Unit]" && key != "Description" && key != "CollectMode" {
			return sessionLeaseError("the retained runtime service changed directive sections")
		}
		seen[key] = value
	}
	for key, value := range map[string]string{"Type": "exec", "NotifyAccess": "main", "FileDescriptorStoreMax": "5", "FileDescriptorStorePreserve": "yes", "Restart": "no", "KillMode": "mixed"} {
		if seen[key] != value {
			return sessionLeaseError("the retained runtime service changed its closed custody properties")
		}
	}
	return nil
}
