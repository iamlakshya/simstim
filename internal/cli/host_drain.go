package cli

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func init() {
	hostSettings = append(hostSettings, hostSetting{key: "drain", about: "refuse new sandboxes while running sessions continue. Give a short reason, or off to clear it.", values: []string{"off"}, check: checkDrainSetting, now: func(client.Host) string { return "current setting" }, apply: applyDrainSetting})
}

func checkDrainSetting(_ client.Host, value string) (string, error) {
	if value == "off" || value != "" && len(value) <= 512 && strings.TrimSpace(value) == value && api.CleanText(value, 512) == value {
		return value, nil
	}
	return "", &client.UserError{Msg: "the drain reason must be one plain line of 1 to 512 bytes", Hint: "Use drain=off to clear it, or give a short reason.", Code: "bad_drain", Kind: client.KindUsage}
}

func applyDrainSetting(ctx context.Context, _ io.Writer, h client.Host, value string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := client.Dial(client.WithoutInstall(ctx), h, false)
	if err != nil {
		return err
	}
	defer c.Close()
	on := value != "off"
	reason := value
	if !on {
		reason = ""
	}
	_, err = c.Drain(ctx, on, reason)
	return err
}
