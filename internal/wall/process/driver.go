package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

const savedSpecName = "process-wall.json"

func prepareDriver(wspec wall.Spec) (wall.Sandbox, error) {
	if strings.TrimSpace(wspec.ID) == "" || strings.TrimSpace(wspec.SessionDir) == "" {
		return nil, errors.New("process wall: ID and SessionDir are required. Try: start sessions through the host helper")
	}
	if err := paths.EnsurePrivateDir(wspec.SessionDir); err != nil {
		return nil, err
	}
	wsHost := filepath.Join(wspec.SessionDir, "work")
	wsGuest := "/work/scratch"
	var extraRO, extraRW []Mount
	for _, m := range wspec.Mounts {
		if m.Write && strings.HasPrefix(m.Guest, "/work/") && wsGuest == "/work/scratch" {
			wsHost = m.Host
			wsGuest = m.Guest
			continue
		}
		pm := Mount{Source: m.Host, Dest: m.Guest, ReadOnly: !m.Write}
		if m.Write {
			extraRW = append(extraRW, pm)
		} else {
			extraRO = append(extraRO, pm)
		}
	}
	if wsGuest == "/work/scratch" {
		if err := paths.EnsurePrivateDir(wsHost); err != nil {
			return nil, err
		}
	}
	stateHost := filepath.Join(wspec.SessionDir, "state")
	if err := paths.EnsurePrivateDir(stateHost); err != nil {
		return nil, err
	}
	ps := Spec{
		SessionID:      wspec.ID,
		HelperExe:      wspec.Helper,
		WorkspaceHost:  wsHost,
		WorkspaceGuest: wsGuest,
		StateHost:      stateHost,
		StateDirs:      wspec.StateDirs,
		ExtraRO:        extraRO,
		ExtraRW:        extraRW,
	}
	if _, err := BuildPlan(ps); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode process wall spec: %w", err)
	}
	root, err := os.OpenRoot(wspec.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("open session dir: %w", err)
	}
	defer root.Close()
	if err := root.WriteFile(savedSpecName, append(b, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("save process wall spec: %w", err)
	}
	return NewSandbox(ps), nil
}

func openDriver(sessionDir string) (wall.Sandbox, error) {
	root, err := os.OpenRoot(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("open session dir %s: %w. Try: check that the session still exists", sessionDir, err)
	}
	defer root.Close()
	b, err := root.ReadFile(savedSpecName)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w. Try: start a new session", savedSpecName, err)
	}
	var ps Spec
	if err := json.Unmarshal(b, &ps); err != nil {
		return nil, fmt.Errorf("decode %s: %w. Try: start a new session", savedSpecName, err)
	}
	return NewSandbox(ps), nil
}
