package egress

import (
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// CanonicalPushTarget validates the canonical host/repository key supplied by
// the laptop's existing repository normaliser. It parses no remote URL and
// invents no second repository alias grammar.
func CanonicalPushTarget(raw string) (string, error) {
	target, err := compileTarget(raw, true)
	if err != nil || target.wildcard || target.isNumeric || target.port != 0 || target.path == "" || target.path == "/" || strings.HasSuffix(target.path, "/") {
		return "", &proto.Error{Code: "bad_push", Message: "push needs one exact canonical repository", Hint: "Use a repository such as github.com/org/repo, without a port, wildcard or trailing slash."}
	}
	if _, spliceOnly, classErr := httpClass(target.host); classErr != nil || spliceOnly {
		return "", &proto.Error{Code: "bad_push", Message: "this repository host cannot be inspected for HTTPS pushes", Hint: "Use an inspectable HTTPS remote, or a narrowly scoped SSH deploy key with its own network grant."}
	}
	return formatCompiledTarget(target), nil
}

func canonicalPushRule(rule api.Rule) (api.Rule, error) {
	target, err := CanonicalPushTarget(rule.Target)
	if err != nil {
		return api.Rule{}, err
	}
	if rule.Mode != "" && rule.Mode != api.PushModePublic {
		return api.Rule{}, &proto.Error{Code: "bad_push", Message: "unknown push acknowledgement mode", Hint: "Omit the mode for a private repository, or use --public after checking the forge."}
	}
	rule.Target = target
	return rule, nil
}

// Push checks a repository through the SAME layer matcher and permit ledger.
// Discovery uses evalPrecheck with no permit. Only the update request reserves
// a DoorPush slot. Presets and agent host lists cannot supply push authority.
func (e *Engine) evaluatePush(repo string, permit *Permit, mode evalMode, at time.Time) Decision {
	canonical, err := CanonicalPushTarget(repo)
	if err != nil {
		return Decision{Rule: "push:invalid"}
	}
	target, err := compileTarget(canonical, true)
	if err != nil {
		return Decision{Rule: "push:invalid"}
	}
	return e.evaluate(Request{Host: target.host, Port: 443, Path: target.path, Now: at, door: api.DoorPush}, permit, mode)
}

func (e *Engine) evaluatePushLocked(repo string, permit *Permit, mode evalMode, at time.Time) Decision {
	canonical, err := CanonicalPushTarget(repo)
	if err != nil {
		return Decision{Rule: "push:invalid"}
	}
	target, err := compileTarget(canonical, true)
	if err != nil {
		return Decision{Rule: "push:invalid"}
	}
	return e.evaluateLocked(Request{Host: target.host, Port: 443, Path: target.path, Now: at, door: api.DoorPush}, permit, mode)
}

func compiledDoor(rule compiledRule) api.Door {
	if rule.door == "" {
		return api.DoorNet
	}
	return rule.door
}
