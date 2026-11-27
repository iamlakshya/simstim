// Package nettarget validates network rule targets without an engine, DNS
// lookup, interface inventory or filesystem access.
package nettarget

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Target is the validated syntax of one network rule target.
type Target struct {
	Host     string
	Port     int
	Path     string
	Wildcard bool
	Prefix   netip.Prefix
}

// Parse validates a host, wildcard, numeric literal or CIDR with its
// optional port and visible path. Numeric targets cannot carry paths.
func Parse(raw string) (Target, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "*" {
		return Target{}, errors.New("target cannot be empty or '*'")
	}
	hostPort, path, err := SplitPath(s)
	if err != nil {
		return Target{}, err
	}
	path, err = CanonicalPath(path)
	if err != nil {
		return Target{}, err
	}
	base, port, err := SplitPort(hostPort)
	if err != nil {
		return Target{}, err
	}
	target := Target{Port: port, Path: path}
	if strings.Contains(base, "/") {
		if path != "" {
			return Target{}, errors.New("a CIDR target cannot carry a URL path")
		}
		prefix, err := ParseRange(base)
		if err != nil {
			return Target{}, err
		}
		target.Prefix = prefix
		return target, nil
	}
	if addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(base, "["), "]")); err == nil && addr.Zone() == "" {
		if path != "" {
			return Target{}, errors.New("an IP target cannot carry a URL path")
		}
		addr = addr.Unmap()
		target.Prefix = netip.PrefixFrom(addr, addr.BitLen())
		return target, nil
	}
	if rest, ok := strings.CutPrefix(base, "*."); ok {
		host, err := NormaliseHost(rest)
		if err != nil || !strings.Contains(host, ".") || isNumericAddr(host) {
			return Target{}, fmt.Errorf("invalid wildcard %q", base)
		}
		if !safeWildcardBoundary(host) {
			return Target{}, errors.New("a wildcard cannot span a public or shared hosting suffix")
		}
		target.Wildcard, target.Host = true, host
		return target, nil
	}
	host, err := NormaliseHost(base)
	if err != nil {
		return Target{}, err
	}
	target.Host = host
	return target, nil
}

// String returns the canonical target syntax used by rule identifiers.
func (target Target) String() string {
	base := target.Host
	if target.Prefix.IsValid() {
		if target.Prefix.Bits() == target.Prefix.Addr().BitLen() {
			base = target.Prefix.Addr().String()
		} else {
			base = target.Prefix.String()
		}
	} else if target.Wildcard {
		base = "*." + base
	}
	if target.Port > 0 {
		if strings.Contains(base, ":") {
			base = "[" + base + "]"
		}
		base += ":" + strconv.Itoa(target.Port)
	}
	return base + (&url.URL{Path: target.Path}).EscapedPath()
}

// Classify validates a target, returns its canonical name and says whether
// its explicit grant requires the client's own-only private-network gate.
// Every numeric exception has that capability, including public addresses.
// This classification does not change the runtime refused-address checks.
func Classify(raw string) (canonical string, private bool, err error) {
	target, err := Parse(raw)
	if err != nil {
		return "", false, err
	}
	local := !strings.Contains(target.Host, ".") || target.Host == "localhost" ||
		strings.HasSuffix(target.Host, ".localhost") || strings.HasSuffix(target.Host, ".local")
	return target.String(), target.Prefix.IsValid() || local, nil
}
