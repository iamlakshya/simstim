package image

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var repositoryPart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var registryHost = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]{1,5})?$`)
var registryTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// Reference separates a registry name from its repository and tag or digest.
type Reference struct{ Registry, Repository, Identifier string }

// ParseReference accepts OCI names and Docker Hub shorthand, never URLs.
func ParseReference(text string) (Reference, error) {
	var r Reference
	if text == "" || len(text) > 4096 || strings.ContainsAny(text, "\\\x00\r\n?#%") || strings.Contains(text, "://") {
		return r, badLayer("invalid image reference %q", text)
	}
	name := text
	if at := strings.LastIndexByte(name, '@'); at >= 0 {
		r.Identifier = name[at+1:]
		name = name[:at]
		if !ValidDigest(r.Identifier) {
			return r, badLayer("image references need a SHA-256 digest")
		}
	}
	if colon := strings.LastIndexByte(name, ':'); colon > strings.LastIndexByte(name, '/') {
		tag := name[colon+1:]
		name = name[:colon]
		if !registryTag.MatchString(tag) {
			return r, badLayer("invalid image tag")
		}
		if r.Identifier == "" {
			r.Identifier = tag
		}
	}
	if r.Identifier == "" {
		r.Identifier = "latest"
	}
	parts := strings.Split(name, "/")
	r.Registry = "registry-1.docker.io"
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		r.Registry = strings.ToLower(parts[0])
		parts = parts[1:]
	}
	if r.Registry == "docker.io" || r.Registry == "index.docker.io" {
		r.Registry = "registry-1.docker.io"
	}
	if !validRegistryHost(r.Registry) {
		return r, badLayer("invalid image registry")
	}
	if len(parts) == 1 && r.Registry == "registry-1.docker.io" {
		parts = append([]string{"library"}, parts...)
	}
	for _, part := range parts {
		if !repositoryPart.MatchString(part) {
			return r, badLayer("invalid image repository")
		}
	}
	r.Repository = strings.Join(parts, "/")
	return r, nil
}

func validRegistryHost(host string) bool {
	if registryHost.MatchString(host) {
		u, err := url.Parse("https://" + host)
		if err != nil {
			return false
		}
		if u.Port() == "" {
			return true
		}
		port, err := strconv.Atoi(u.Port())
		return err == nil && port > 0 && port <= 65535
	}
	u, err := url.Parse("https://" + host)
	if err != nil || !strings.HasPrefix(host, "[") || u.Path != "" || u.User != nil {
		return false
	}
	address, err := netip.ParseAddr(u.Hostname())
	if err != nil || !address.Is6() {
		return false
	}
	if u.Port() == "" {
		return true
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535
}
