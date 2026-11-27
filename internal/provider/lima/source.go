package lima

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/provider"
	"go.yaml.in/yaml/v3"
)

func plainSourceUnknown() error {
	return &client.UserError{Code: "source_inventory", Msg: "the original Lima plain instance or applicable sharing configuration cannot be checked", Hint: "Use the original tool-created plain instance and repair its local configuration before running local Git.", Kind: client.KindRefused}
}

func capturePlainSource(ctx context.Context, h client.Host, in *instance) (_ *client.MachineSourceProvenance, inspection *provider.SourceInspection, returned error) {
	if in == nil || in.Config == nil || in.Config.Plain == nil || !*in.Config.Plain || in.Dir == "" || in.Name != h.Machine.ID {
		return nil, nil, plainSourceUnknown()
	}
	config, pin, err := provider.CaptureSourceConfigPinned(in.Dir)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		returned = errors.Join(returned, pin.Close())
		if returned != nil && inspection != nil {
			returned = errors.Join(returned, inspection.Close())
		}
	}()
	source := &client.MachineSourceProvenance{Version: client.MachineSourceVersion, Kind: "lima-plain", ResourceID: in.Name, Config: config}
	if _, err := client.NormaliseMachineSource("lima", source); err != nil {
		return nil, nil, err
	}
	h.Machine.Source = source
	inspection, err = InspectSource(ctx, h, nil)
	if err != nil {
		return nil, nil, err
	}
	return source, inspection, nil
}

// InspectSource uses actual closed-create plain provenance plus the SAME
// instance/config native identity and current applicable file witnesses.
// It reads metadata/configuration only and starts no VM or provider process.
func InspectSource(ctx context.Context, h client.Host, _ []string) (_ *provider.SourceInspection, err error) {
	if h.Machine == nil {
		return nil, plainSourceUnknown()
	}
	source, err := client.NormaliseMachineSource("lima", h.Machine.Source)
	if err != nil {
		return nil, err
	}
	if source.ResourceID != h.Machine.ID {
		return nil, plainSourceUnknown()
	}
	pin, err := provider.PinSourceConfig(source.Config)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, pin.Close())
		}
	}()
	sum := sha256.New()
	for index, path := range []string{filepath.Join(source.Config.Path, "lima.yaml"), filepath.Join(filepath.Dir(source.Config.Path), "_config", "default.yaml"), filepath.Join(filepath.Dir(source.Config.Path), "_config", "override.yaml")} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, readErr := readSourceYAML(path)
		if errors.Is(readErr, os.ErrNotExist) && index != 0 {
			sum.Write([]byte{byte(index), 0})
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		var config struct {
			Plain *bool     `yaml:"plain"`
			Base  yaml.Node `yaml:"base"`
		}
		if yaml.Unmarshal(data, &config) != nil || config.Base.Kind != 0 {
			return nil, plainSourceUnknown()
		}
		if index == 0 && (config.Plain == nil || !*config.Plain) || index == 2 && config.Plain != nil && !*config.Plain {
			return nil, plainSourceUnknown()
		}
		sum.Write([]byte{byte(index), 1})
		sum.Write(data)
	}
	// Existing provider start witnesses distinguish currently running config
	// from edits which only apply on restart. Unknown/stale is never empty.
	in := &instance{Dir: source.Config.Path}
	if _, ok := modTime(filepath.Join(in.Dir, "ha.pid")); ok && changedSinceStart(in) {
		return nil, plainSourceUnknown()
	}
	if err := pin.Revalidate(); err != nil {
		return nil, err
	}
	return &provider.SourceInspection{Config: pin, Stamp: hex.EncodeToString(sum.Sum(nil))}, nil
}

func readSourceYAML(path string) (_ []byte, err error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := files.OpenRegular(root, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, plainSourceUnknown()
	}
	return data, nil
}
