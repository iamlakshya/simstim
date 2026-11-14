package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// shouldRestrictSSHEnv keeps a host's environment private unless the saved
// entry and its current tier both say that the host belongs to the user.
// A name alone is insufficient: an unsaved replacement may name another
// endpoint while an older entry of that name still has tier own.
func shouldRestrictSSHEnv(ctx context.Context, h Host) (bool, error) {
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if origin, _ := ctx.Value(sshOriginKey{}).(*Host); origin != nil {
		if origin.Name != h.Name {
			return true, (HostChange{Kind: "connect", Was: origin, Now: origin}).changed(&h)
		}
		h = *origin
	}
	if staging(h.Name) {
		return true, nil
	}
	hosts, err := LoadHosts()
	if err != nil {
		return true, err
	}
	isSaved := false
	for _, saved := range hosts {
		if saved.Name == h.Name && SameEntry(saved, h) {
			isSaved = true
			break
		}
	}
	if !isSaved {
		return true, nil
	}
	// Argument assembly can hold the pin lock. Reading the tier must never
	// start migration here, because migration takes that same pin lock.
	// Normal host entry points migrate first. Until then, remain restricted.
	ts, err := readTiersFile()
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	return ts.Of(h.Name) != TierOwn || ts.IDChanged(h.Name), nil
}

// CheckSSHEnvironment checks the effective SSH settings before a connection
// to a restricted host starts. args are the connection's options, before
// its destination. It connects to nothing and never includes an environment
// value in its error.
func CheckSSHEnvironment(ctx context.Context, target string, args []string) error {
	vals, _, err := sshG(ctx, args, target)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || len(vals) == 0 {
		return sshEnvironmentReadError(target)
	}
	return refuseSSHSetEnv(target, vals)
}

func sshEnvironmentReadError(target string) *UserError {
	return &UserError{
		Msg:  "cannot read all of what ssh -G prints, so the client does not connect",
		Hint: "Check your SSH config with " + sshGCommand(target) + ", then try again.",
		Code: "ssh_config_read",
	}
}

func sshGCommand(target string) string {
	return "ssh -G " + strings.TrimPrefix(SSHCommand(target), "ssh ")
}

// An SSH master keeps options from its first connection. Separate both
// environment policies from each other and from every pre-policy master.
// The host index still owns all of their epochs for pin retirement.
func controlEnvKey(key string, restricted bool) string {
	domain := "env-own-v1"
	if restricted {
		domain = "env-restricted-v1"
	}
	return controlHash(domain, key)
}

func refuseSSHSetEnv(target string, vals map[string][]string) error {
	if _, present := vals["setenv"]; !present {
		return nil
	}
	return &UserError{
		Msg:  fmt.Sprintf("your SSH config uses SetEnv for %s, which could send private values to its owner", CleanLine(target)),
		Hint: "Remove SetEnv from this target's SSH config, or finish adding the host without SetEnv before setting its tier to own.",
		Code: "ssh_setenv", Kind: KindRefused,
	}
}

func (s *SSH) checkSSHEnvironment(ctx context.Context, args []string) error {
	ctx = s.policyContext(ctx)
	restrict, err := shouldRestrictSSHEnv(ctx, s.Host)
	if err != nil || !restrict {
		return err
	}
	return CheckSSHEnvironment(ctx, s.Host.SSH, args)
}
