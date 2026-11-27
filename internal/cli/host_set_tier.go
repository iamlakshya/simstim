package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func checkTier(_ client.Host, v string) (string, error) {
	t, err := client.ParseTier(v)
	if err != nil {
		return "", err
	}
	return t.String(), nil
}

func tierNowContext(ctx context.Context, h client.Host) (string, error) {
	hosts, err := client.LoadHosts()
	if err != nil {
		return "", err
	}
	tiers, err := client.LoadTiersContext(ctx, hosts)
	if err != nil {
		return "", err
	}
	return tiers.Of(h.Name).String(), nil
}

func checkSetConfirm(ctx context.Context, p *prompter, h client.Host, pairs []pair, confirm string) error {
	var tierValue string
	for _, pr := range pairs {
		if pr.s.key == "tier" {
			tierValue = pr.value
		}
	}
	if confirm != "" && tierValue == "" {
		return &client.UserError{
			Msg:  "--confirm applies only when raising a host's tier with tier=",
			Hint: brand.Name + " host set " + shellWord(h.Name) + " tier=own --confirm " + shellWord(h.Name),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	if confirm != "" && confirm != h.Name {
		return &client.UserError{
			Msg:  fmt.Sprintf("--confirm names %s, and the host is %s", client.ShellWord(confirm), h.Name),
			Hint: brand.Name + " host set " + shellWord(h.Name) + " tier=" + tierValue + " --confirm " + shellWord(h.Name),
			Code: "bad_value",
			Kind: client.KindUsage,
		}
	}
	if tierValue == "" {
		return nil
	}
	hosts, err := client.LoadHosts()
	if err != nil {
		return err
	}
	tiers, err := client.LoadTiersContext(ctx, hosts)
	if err != nil {
		return err
	}
	oldTier := tiers.Of(h.Name)
	newTier, _ := client.ParseTier(tierValue)
	if newTier > oldTier && confirm == "" && !p.on {
		return p.needs(qRaiseTier, fmt.Sprintf("the host's typed name to raise %s's tier from %s to %s", h.Name, oldTier, newTier), "--confirm", shellWord(h.Name))
	}
	return nil
}

func applyTierSetting(ctx context.Context, w io.Writer, h client.Host, v string) error {
	sc, _ := ctx.Value(setCallKey{}).(setCall)
	if sc.p == nil {
		sc.p = &prompter{ctx: ctx, out: w, path: brand.Name + " host set"}
	}
	hosts, err := client.LoadHosts()
	if err != nil {
		return err
	}
	tiers, err := client.LoadTiersContext(ctx, hosts)
	if err != nil {
		return err
	}
	oldTier := tiers.Of(h.Name)
	newTier, err := client.ParseTier(v)
	if err != nil {
		return err
	}
	if newTier <= oldTier {
		return client.SetTierContext(ctx, h.Name, newTier, "", "")
	}
	fmt.Fprintf(w, "Checking %s (%s)…\n", h.Name, h.Target())
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := client.Dial(client.WithTierQuestion(client.WithoutInstall(dctx), nil), h, sc.p.on)
	if err != nil {
		return err
	}
	hostID := conn.HostID()
	conn.Close()
	if sc.confirm == "" {
		ok, err := sc.p.typedName(fmt.Sprintf("Type %s to raise its tier from %s to %s, or press Enter to keep %s:", h.Name, oldTier, newTier, oldTier), h.Name)
		switch {
		case err != nil:
			return err
		case !ok:
			return sc.p.cancel()
		case stopped(ctx):
			return errInterrupted
		}
	}
	var pinFp string
	if pin, err := client.PinOf(h.Name); err == nil {
		pinFp = pin.Fingerprint
	}
	return client.SetTierContext(ctx, h.Name, newTier, hostID, pinFp)
}
