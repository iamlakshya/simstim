package client

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type explicitRouteHopV1 struct {
	logical, user, hostname string
	port                    uint16
	values                  map[string][]string
}
type explicitRouteSourceV1 struct {
	readset  *explicitRouteReadsetV1
	logical  string
	hops     []explicitRouteHopV1
	wrapper  string
	snapshot *routeConfigSnapshotV1
	closed   bool
}

// resolveExplicitJoinedRouteV1 is a private original-local-selection producer.
// It is not an exported route/command constructor and opens no connection.
func resolveExplicitJoinedRouteV1(ctx context.Context, lease *InviteAcceptance, path string) (result *explicitRouteSourceV1, failure error) {
	if err := lease.checkOriginalInviteV1(ctx); err != nil {
		return nil, err
	}
	source, err := captureExplicitRouteReadsetV1(ctx, path)
	if err != nil {
		return nil, err
	}
	route := &explicitRouteSourceV1{readset: source, logical: lease.original.Name}
	keep := false
	defer func() {
		if !keep {
			failure = joinRouteErrorsV1(failure, route.close())
		}
	}()
	route.snapshot, err = freezeRouteConfigV1(ctx, source)
	if err != nil {
		return nil, err
	}
	route.wrapper = route.snapshot.wrapper
	if err = route.check(ctx); err != nil {
		return nil, err
	}
	first, _, err := sshG(ctx, []string{"-F", route.wrapper, "-o", "BatchMode=yes"}, route.logical)
	if err != nil {
		return nil, err
	}
	if err = route.check(ctx); err != nil {
		return nil, err
	}
	jump := strings.TrimSpace(firstValueV1(first, "proxyjump"))
	if jump == "" || jump == "none" {
		return nil, routeSourceErrorV1("the selected existing config has no ProxyJump route for this invite name")
	}
	var hops []explicitRouteHopV1
	for _, item := range strings.Split(jump, ",") {
		if len(hops) >= 8 {
			return nil, routeSourceErrorV1("the explicit ProxyJump chain exceeds eight bastions")
		}
		hop, err := resolveExplicitRouteHopV1(ctx, route, item)
		if err != nil {
			return nil, err
		}
		if nested := firstValueV1(hop.values, "proxyjump"); nested != "" && nested != "none" {
			return nil, routeSourceErrorV1("nested per-bastion ProxyJump must be written as one explicit ordered ProxyJump chain for this join")
		}
		for _, prior := range hops {
			if prior.logical == hop.logical {
				return nil, routeSourceErrorV1("the explicit ProxyJump chain repeats a bastion")
			}
		}
		hops = append(hops, hop)
	}
	// Re-resolve the exact original closure. No changed projection is adopted.
	again, _, err := sshG(ctx, []string{"-F", route.wrapper, "-o", "BatchMode=yes"}, route.logical)
	if err != nil {
		return nil, err
	}
	if err = route.check(ctx); err != nil {
		return nil, err
	}
	if firstValueV1(again, "proxyjump") != firstValueV1(first, "proxyjump") {
		return nil, routeSourceErrorV1("the explicit route projection changed during discovery")
	}
	route.hops = hops
	keep = true
	return route, nil
}
func resolveExplicitRouteHopV1(ctx context.Context, source *explicitRouteSourceV1, item string) (explicitRouteHopV1, error) {
	var hop explicitRouteHopV1
	if strings.ContainsAny(item, "%$\r\n\x00 \\'") {
		return hop, routeSourceErrorV1("a ProxyJump target has unsupported dynamic or command syntax")
	}
	parsed, err := url.Parse("ssh://" + item)
	if err != nil || parsed.Hostname() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return hop, routeSourceErrorV1("a ProxyJump target is malformed")
	}
	if parsed.User != nil {
		if _, password := parsed.User.Password(); password {
			return hop, routeSourceErrorV1("a ProxyJump target must not contain a password")
		}
	}
	logical := parsed.Hostname()
	if net.ParseIP(logical) == nil {
		for _, c := range logical {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
				return hop, routeSourceErrorV1("a ProxyJump alias is invalid")
			}
		}
	}
	arguments := []string{"-F", source.wrapper, "-o", "BatchMode=yes"}
	if parsed.User != nil {
		arguments = append(arguments, "-l", parsed.User.Username())
	}
	if parsed.Port() != "" {
		number, err := strconv.ParseUint(parsed.Port(), 10, 16)
		if err != nil || number == 0 {
			return hop, routeSourceErrorV1("a ProxyJump port is invalid")
		}
		arguments = append(arguments, "-p", parsed.Port())
	}
	if err = source.check(ctx); err != nil {
		return hop, err
	}
	values, _, err := sshG(ctx, arguments, logical)
	if err != nil {
		return hop, err
	}
	if err = source.check(ctx); err != nil {
		return hop, err
	}
	port, err := strconv.ParseUint(firstValueV1(values, "port"), 10, 16)
	if err != nil || port == 0 {
		return hop, routeSourceErrorV1("the native bastion resolver returned an invalid port")
	}
	hostname, user := firstValueV1(values, "hostname"), firstValueV1(values, "user")
	if !routeResolverAtomV1(hostname) || !routeResolverAtomV1(user) {
		return hop, routeSourceErrorV1("the native bastion resolver returned incomplete identity")
	}
	if command := firstValueV1(values, "proxycommand"); command != "" && command != "none" {
		return hop, routeSourceErrorV1("the native bastion resolver returned an executable transport")
	}
	return explicitRouteHopV1{logical, user, hostname, uint16(port), values}, nil
}
func firstValueV1(values map[string][]string, key string) string { return first(values, key) }
func joinRouteErrorsV1(left, right error) error {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return fmt.Errorf("%v; original explicit-route cleanup: %w", left, right)
}
func (s *explicitRouteSourceV1) check(ctx context.Context) error {
	if s == nil || s.closed {
		return routeSourceErrorV1("the original explicit route ended")
	}
	if err := s.readset.check(ctx); err != nil {
		return err
	}
	return s.snapshot.check(ctx)
}
func (s *explicitRouteSourceV1) close() error {
	if s == nil || s.closed {
		return nil
	}
	failure := joinRouteErrorsV1(s.readset.close(), s.snapshot.close())
	if failure == nil {
		s.closed = true
	}
	return failure
}

func routeResolverAtomV1(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._-:@[]", char)) {
			return false
		}
	}
	return true
}
