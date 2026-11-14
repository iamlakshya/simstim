package host

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

func purgeRootPath(control *placementControl, root string) (string, error) {
	switch root {
	case "state":
		return control.PersistentRoots.State, nil
	case "data":
		return control.PersistentRoots.Data, nil
	case "config":
		return control.PersistentRoots.Config, nil
	}
	if key, ok := strings.CutPrefix(root, "runtime:"); ok {
		receipt, known := control.RuntimeRoots[key]
		if !known || len(receipt.extra) != 0 || runtimeRootKey(receipt.Path) != key || !validRuntimeRootPath(receipt.Path) {
			return "", placementError("the runtime cleanup has no recorded native path role")
		}
		return receipt.Path, nil
	}
	if key, ok := strings.CutPrefix(root, "parent:"); ok && slices.Contains(api.BaselineParentKeys(), key) {
		roles, err := baselineParentPaths()
		if err != nil {
			return "", err
		}
		return filepath.Clean(roles[key]), nil
	}
	return "", placementError("the purge resource has an unknown root role")
}

func validPurgeRelative(raw []byte) bool {
	path := string(raw)
	return len(raw) > 0 && !strings.ContainsRune(path, 0) && filepath.IsLocal(path) && filepath.Clean(path) == path
}

func baselineParentPaths() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, placementError("cannot identify baseline parent roles")
	}
	xdg := func(name, fallback string) string {
		if value := os.Getenv(name); filepath.IsAbs(value) {
			return value
		}
		return home + "/" + fallback
	}
	return map[string]string{
		"local": home + "/.local", "local_share": home + "/.local/share", "local_state": home + "/.local/state", "config": home + "/.config",
		"systemd": home + "/.config/systemd", "systemd_user": home + "/.config/systemd/user", "default_wants": home + "/.config/systemd/user/default.target.wants", "timers_wants": home + "/.config/systemd/user/timers.target.wants", "terminfo": home + "/.terminfo",
		"xdg_data": xdg("XDG_DATA_HOME", ".local/share"), "xdg_state": xdg("XDG_STATE_HOME", ".local/state"), "xdg_config": xdg("XDG_CONFIG_HOME", ".config"),
	}, nil
}

func validPurgeGitTip(tip string) bool {
	return (len(tip) == 40 || len(tip) == 64) && strings.Trim(tip, "0123456789abcdef") == ""
}
