package client

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/brand"
)

// ResolveHost resolves a named start, the last attached host, or a local
// default. It performs no dial, wake, value read, reservation or admission.
// The selected connection must still capture its actual accepted Source.
func ResolveHost(ctx context.Context, selected string) (Host, error) {
	if err := ctx.Err(); err != nil {
		return Host{}, err
	}
	file, err := loadHostsFile()
	if err != nil {
		return Host{}, err
	}
	if selected == "-" {
		last, ok := LoadLast()
		if !ok {
			return Host{}, &UserError{Msg: "no session has been attached yet", Hint: brand.Name + " new <host>", Code: "no_last_session", Kind: KindNotFound}
		}
		selected = last.Host
	}
	if selected != "" {
		host, err := findPlacementHost(file.Hosts, selected)
		if err != nil {
			return Host{}, err
		}
		return host, ctx.Err()
	}
	name := os.Getenv(brand.Env("DEFAULT_HOST"))
	if name == "" {
		name = file.Default
	}
	tiers, err := LoadTiersContext(ctx, file.Hosts)
	if err != nil {
		return Host{}, err
	}
	if name != "" {
		host, err := findPlacementHost(file.Hosts, name)
		if err != nil {
			return Host{}, err
		}
		if err := Allow(host, tiers.Of(host.Name), Outbound{Kind: OutboundDefaultHost}, DefaultSettings()); err != nil {
			return Host{}, err
		}
		return host, ctx.Err()
	}
	var only *Host
	for _, host := range file.Hosts {
		if !ValidHostName(host.Name) || tiers.Of(host.Name) == TierShared {
			continue
		}
		if only != nil {
			return Host{}, defaultHostRequired()
		}
		copy := cloneObservationHost(host)
		only = &copy
	}
	if only == nil {
		return Host{}, defaultHostRequired()
	}
	return *only, ctx.Err()
}

func findPlacementHost(hosts []Host, name string) (Host, error) {
	if !ValidHostName(name) {
		return Host{}, badHostNameError(name)
	}
	var found *Host
	for _, host := range hosts {
		if host.Name != name {
			continue
		}
		if found != nil {
			return Host{}, &UserError{Msg: "the saved host name is repeated", Hint: "Remove the duplicate host entry before starting a session.", Code: "config_unusable", Kind: KindUsage}
		}
		copy := cloneObservationHost(host)
		found = &copy
	}
	if found == nil {
		return Host{}, &UserError{Msg: "the selected host is no longer saved", Hint: brand.Name + " host ls", Code: "host_not_found", Kind: KindNotFound}
	}
	return *found, nil
}

func defaultHostRequired() error {
	return &UserError{Msg: "no default host is selected", Hint: brand.Name + " host default <host>, or " + brand.Name + " new <host>", Code: "host_required", Kind: KindUsage}
}

// DefaultHost returns the saved default, without the environment override.
func DefaultHost(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, err := savedDefaultHost()
	if err != nil {
		return "", err
	}
	return name, ctx.Err()
}
