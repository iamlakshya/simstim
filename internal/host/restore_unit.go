package host

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/iamlakshya/simstim/internal/brand"
)

// restoreUnit is the systemd user unit that runs restore at boot.
func restoreUnit() string { return brand.Name + "-restore.service" }

// restoreUnitText is the restore unit's file.
func restoreUnitText() string {
	return fmt.Sprintf(`[Unit]
Description=Restore %s sessions after a restart
After=default.target

[Service]
Type=oneshot
ExecStart=%s restore

[Install]
WantedBy=default.target
`, brand.DisplayName, currentHelper())
}

// InstallRestoreUnit writes and enables the boot-time restore unit.
func InstallRestoreUnit() Check {
	c := Check{Name: "restore after restart"}
	if !SystemdUser() {
		c.Detail = "no systemd user manager; sessions are not restored after a restart"
		return c
	}
	unit := restoreUnitText()
	if err := writeFootprintedUnitV1(context.Background(), restoreUnit(), unit); err != nil {
		c.Detail, c.Fix = err.Error(), "Keep the host state and inspect the preserved restore unit before retrying setup."
		return c
	}
	_ = exec.CommandContext(context.Background(), "systemctl", "--user", "daemon-reload").Run()
	err := enableFootprintedUnitV1(context.Background(), "default_wants", restoreUnit())
	if err != nil {
		c.Detail = "could not enable it: " + err.Error()
		return c
	}
	c.OK, c.Detail = true, "sessions come back after a host restart"
	return c
}
