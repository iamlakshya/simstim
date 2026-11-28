package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/sshserve"
)

func sshConfigCmd() *cobra.Command {
	var remove, yes bool
	cmd := &cobra.Command{
		Use:   "ssh-config",
		Short: "Set up SSH for editors, scp, sftp and rsync into sessions",
		Long: "Writes ~/.ssh/" + brand.Name + "/config and ~/.ssh/" + brand.Name + "/known_hosts so that VS Code, Cursor, Zed, scp,\n" +
			"sftp, rsync and plain ssh can connect to <session>.<host>." + brand.Name + " through this client.\n" +
			"SSH ends in this client, so no SSH daemon runs in the session.",
		Example: "  " + brand.Name + " ssh-config\n" +
			"  " + brand.Name + " ssh-config --remove",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSSHConfig(cmd, remove, yes)
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the generated SSH files and the Include line")
	addYes(cmd, &yes, "add the Include line to ~/.ssh/config without asking")
	return cmd
}

func runSSHConfig(cmd *cobra.Command, remove, yes bool) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return &client.UserError{
			Msg:  "could not find your home folder",
			Hint: "Set HOME and run ssh-config again.",
			Code: "no_home",
		}
	}
	if remove {
		return removeSSHConfig(cmd, home)
	}
	if err := checkLocalOpenSSH(cmd.Context()); err != nil {
		return err
	}
	key, err := sshserve.EnsureHostKey(client.StateDir())
	if err != nil {
		return err
	}
	cfgPath, khPath, err := sshserve.WriteFiles(home, key, proxyBinary())
	if err != nil {
		return err
	}
	say(cmd, "Wrote %s and %s.\n", cfgPath, khPath)
	say(cmd, "%s\n", sshserve.TrustNotice)
	return maybeAddSSHInclude(cmd, home, cfgPath, yes)
}

func removeSSHConfig(cmd *cobra.Command, home string) error {
	remInc, remDir, err := sshserve.Remove(home)
	if err != nil {
		return err
	}
	if !remInc && !remDir {
		say(cmd, "Nothing to remove.\n")
		return nil
	}
	say(cmd, "Removed SSH configuration for *.%s.\n", brand.Name)
	return nil
}

func checkLocalOpenSSH(ctx context.Context) error {
	err := sshserve.CheckOpenSSH(ctx, "ssh")
	if errors.Is(err, sshserve.ErrOldOpenSSH) {
		return &client.UserError{
			Msg:  "This ssh passes shell characters in host names to ProxyCommand",
			Hint: "Update OpenSSH to 9.6 or later.",
			Code: "ssh_old",
		}
	}
	return err
}

func maybeAddSSHInclude(cmd *cobra.Command, home, cfgPath string, yes bool) error {
	addInclude := yes
	if !yes {
		p := prompts(cmd)
		ok, perr := p.confirm(qSSHConfig, fmt.Sprintf("Add %q to the top of ~/.ssh/config?", sshserve.IncludeLine()), "a yes to edit ~/.ssh/config")
		if perr != nil {
			return perr
		}
		addInclude = ok
	}
	if !addInclude {
		say(cmd, "Left ~/.ssh/config unchanged. Add %q at its top, or run %s ssh-config --yes.\n", sshserve.IncludeLine(), brand.Name)
		return nil
	}
	changed, err := sshserve.EnsureInclude(home)
	if err != nil {
		return err
	}
	if changed {
		say(cmd, "Added %s to ~/.ssh/config.\n", sshserve.IncludeLine())
	} else {
		say(cmd, "~/.ssh/config already includes %s.\n", cfgPath)
	}
	return nil
}

// proxyBinary returns brand.Name when it is on PATH, or the current
// executable's shell-quoted path otherwise so ProxyCommand always resolves it.
func proxyBinary() string {
	if _, err := exec.LookPath(brand.Name); err == nil {
		return brand.Name
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		return shellWord(exe)
	}
	return brand.Name
}

func sshProxyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ssh-proxy <target>",
		Short:   "Serve SSH on stdin and stdout for OpenSSH ProxyCommand",
		Long:    "Internal command run by OpenSSH ProxyCommand for <session>.<host>." + brand.Name + ".",
		Example: "  " + brand.Name + " ssh-proxy auth-fix.my-vps." + brand.Name,
		Hidden:  true,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessRef, hostName, err := sshserve.ParseTarget(args[0])
			if err != nil {
				return &client.UserError{
					Msg:  err.Error(),
					Hint: fmt.Sprintf("Use <session>.<host>.%s, such as auth-fix.my-vps.%s.", brand.Name, brand.Name),
					Code: "bad_address",
					Kind: client.KindUsage,
				}
			}
			h, err := client.FindHost(hostName)
			if err != nil {
				return err
			}
			cctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			conn, err := client.Dial(cctx, h, false)
			if err != nil {
				cancel()
				return err
			}
			list, err := listSessions(cctx, conn)
			var s *client.Session
			if err == nil {
				s, err = findIn(cmd, list, sessRef, h.Name)
			}
			hasSFTP := conn.HasFeature(api.FeatureSFTP)
			hasExec := conn.HasFeature(api.FeatureSSHExec)
			conn.Close()
			cancel()
			if err != nil {
				return err
			}
			if !hasSFTP || !hasExec {
				return &client.UserError{
					Msg:  "the helper on " + h.Name + " is too old for ssh-proxy",
					Hint: brand.Name + " host add " + h.Name + " (updates the helper)",
					Code: "helper_old",
				}
			}
			tr, err := client.HostTransport(cmd.Context(), h, false)
			if err != nil {
				return err
			}
			key, err := sshserve.EnsureHostKey(client.StateDir())
			if err != nil {
				return err
			}
			backend := &client.SessionSSHBackend{
				Host:      h,
				SessionID: s.ID,
				Transport: tr,
				Walled:    api.IsWalled(s.Wall),
			}
			return sshserve.ServeStdio(cmd.Context(), key, backend)
		},
	}
}
