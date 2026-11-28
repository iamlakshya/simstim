package gvisor

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

const savedSpecName = "gvisor-wall.json"

func prepareDriver(wspec wall.Spec) (wall.Sandbox, error) {
	if strings.TrimSpace(wspec.ID) == "" || strings.TrimSpace(wspec.SessionDir) == "" {
		return nil, errors.New("gvisor wall: ID and SessionDir are required. Try: start sessions through the host helper")
	}
	if wspec.Image != nil {
		return nil, errors.New("gvisor wall: container root images are not supported yet (spec 21.4). Try: omit --image or use --wall microvm")
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
		gm := Mount{Source: m.Host, Dest: m.Guest, ReadOnly: !m.Write}
		if m.Write {
			extraRW = append(extraRW, gm)
		} else {
			extraRO = append(extraRO, gm)
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
	gs := Spec{
		SessionID:      wspec.ID,
		SessionDir:     wspec.SessionDir,
		HelperExe:      wspec.Helper,
		WorkspaceHost:  wsHost,
		WorkspaceGuest: wsGuest,
		StateHost:      stateHost,
		StateDirs:      wspec.StateDirs,
		ExtraRO:        extraRO,
		ExtraRW:        extraRW,
	}
	bundleDir := filepath.Join(wspec.SessionDir, "bundle")
	if _, err := BuildBundlePlan(bundleDir, gs); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode gvisor wall spec: %w", err)
	}
	root, err := os.OpenRoot(wspec.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("open session dir: %w", err)
	}
	defer root.Close()
	if err := root.WriteFile(savedSpecName, append(b, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("save gvisor wall spec: %w", err)
	}
	return NewSandbox(gs), nil
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
	var gs Spec
	if err := json.Unmarshal(b, &gs); err != nil {
		return nil, fmt.Errorf("decode %s: %w. Try: start a new session", savedSpecName, err)
	}
	if gs.SessionDir == "" {
		gs.SessionDir = sessionDir
	}
	return NewSandbox(gs), nil
}
