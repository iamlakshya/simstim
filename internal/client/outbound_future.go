package client

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

func allowPool(h Host, t Tier, o Outbound) error {
	switch o.Name {
	case "", "untrusted", "throwaway", "shared":
	default:
		return &UserError{
			Msg:  fmt.Sprintf("unknown pool intent %q", CleanLine(o.Name)),
			Hint: "Use an empty pool intent, untrusted, throwaway or shared.",
			Code: "bad_grant",
			Kind: KindUsage,
		}
	}
	hName := hostDisplayName(h)
	inPool, poolSet := h.Pool()
	if poolSet && !inPool {
		return &UserError{
			Msg:  hName + " is excluded from the auto pool",
			Hint: fmt.Sprintf("Pick %s by name: %s new %s.", hName, brand.Name, hostCommandArg(h)),
			Code: "tier_refused",
			Kind: KindRefused,
		}
	}
	switch t {
	case TierOwn:
		return nil
	case TierThrowaway:
		if o.Name == "untrusted" || o.Name == "throwaway" {
			return nil
		}
		return &UserError{
			Msg:  hName + " is a throwaway host, so auto placement uses it only for --untrusted",
			Hint: fmt.Sprintf("Pass --untrusted, or name the host: %s new %s.", brand.Name, hostCommandArg(h)),
			Code: "tier_refused",
			Kind: KindRefused,
		}
	default:
		if o.Name == "shared" || (poolSet && inPool) {
			return nil
		}
		return &UserError{
			Msg:  hName + " is a shared host, so it is not in the auto pool",
			Hint: fmt.Sprintf("Name the host directly: %s new %s.", brand.Name, hostCommandArg(h)),
			Code: "tier_refused",
			Kind: KindRefused,
		}
	}
}

func allowDefaultHost(h Host, t Tier, o Outbound) error {
	if err := allowCleanOptionalName(o, "default-host"); err != nil {
		return err
	}
	if t == TierOwn || t == TierThrowaway {
		return nil
	}
	hName := hostDisplayName(h)
	return &UserError{
		Msg:  hName + " is a shared host, so it cannot be the default",
		Hint: fmt.Sprintf("%s host set %s tier=own, only if nobody else has root on %s.", brand.Name, hostCommandArg(h), hName),
		Code: "tier_refused",
		Kind: KindRefused,
	}
}

func allowRootRun(h Host, t Tier, o Outbound) error {
	if err := allowCleanOptionalName(o, "root-run"); err != nil {
		return err
	}
	if t == TierOwn || t == TierThrowaway || o.Confirmed {
		return nil
	}
	hName := hostDisplayName(h)
	return &ConfirmError{
		Prompt: fmt.Sprintf("Type %s to run the root step on this shared host:", hName),
		Kind:   ConfirmHostName,
		Host:   hName,
		Grant:  o,
		Err: &UserError{
			Msg:  hName + " is a shared host, so its owner runs the root step",
			Hint: fmt.Sprintf("%s host prepare %s --root-step print", brand.Name, hostCommandArg(h)),
			Code: "tier_refused",
			Kind: KindRefused,
		},
	}
}

func allowSilentUpgrade(h Host, t Tier, o Outbound) error {
	if err := allowCleanOptionalName(o, "silent-upgrade"); err != nil {
		return err
	}
	if t == TierOwn || t == TierThrowaway || o.Confirmed {
		return nil
	}
	hName := hostDisplayName(h)
	return &ConfirmError{
		Prompt: fmt.Sprintf("Upgrade the runtime on %s?", hName),
		Kind:   ConfirmYesNo,
		Host:   hName,
		Grant:  o,
		Err: &UserError{
			Msg:  hName + " is a shared host, so runtime upgrades ask first",
			Hint: fmt.Sprintf("Run %s host prepare %s in a terminal.", brand.Name, hostCommandArg(h)),
			Code: "tier_refused",
			Kind: KindRefused,
		},
	}
}

func wallLevelRank(level string) int {
	switch level {
	case "none":
		return 1
	case "process":
		return 2
	case "gvisor":
		return 3
	case "microvm":
		return 4
	default:
		return 0
	}
}

func allowFloor(h Host, t Tier, o Outbound) error {
	rank := wallLevelRank(o.Name)
	if rank == 0 {
		return &UserError{
			Msg:  fmt.Sprintf("%q is not a sandbox wall level", CleanLine(o.Name)),
			Hint: "Use none, process, gvisor or microvm.",
			Code: "bad_floor",
			Kind: KindUsage,
		}
	}
	minLevel, minRank := "process", 2
	if t == TierShared {
		minLevel, minRank = "gvisor", 3
	}
	if rank >= minRank {
		return nil
	}
	hName := hostDisplayName(h)
	article := "a " + t.String()
	if t == TierOwn {
		article = "an own"
	}
	return &UserError{
		Msg:  fmt.Sprintf("%s is %s host, whose lowest floor is %s, not %s", hName, article, minLevel, o.Name),
		Hint: fmt.Sprintf("%s host set %s floor=%s", brand.Name, hostCommandArg(h), minLevel),
		Code: "wall_floor",
		Kind: KindRefused,
	}
}

func allowTemplate(h Host, t Tier, o Outbound) error {
	if hasControlRune(o.Name) || o.Name != "" && strings.TrimSpace(o.Name) == "" {
		return &UserError{Msg: "template grant requires a valid template name", Hint: "Pass a template name with --template.", Code: "bad_grant", Kind: KindUsage}
	}
	if t == TierOwn || t == TierThrowaway || strings.TrimSpace(o.Name) != "" {
		return nil
	}
	hName := hostDisplayName(h)
	return &UserError{Msg: hName + " is a shared host, so it gets a user template only with --template on that start",
		Hint: fmt.Sprintf("Pass --template <name> on %s new %s.", brand.Name, hostCommandArg(h)), Code: "tier_refused", Kind: KindRefused}
}
