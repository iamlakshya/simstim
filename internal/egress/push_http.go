package egress

import (
	"net/url"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type pushHTTPRoute struct {
	repo      string
	discovery bool
	valid     bool
}

// The protocol path is extracted after the one HTTP canonicaliser. The
// repository key follows the existing convention of removing one .git suffix.
func gitPushHTTPRoute(request Request) (pushHTTPRoute, bool) {
	parts := strings.Split(strings.TrimSuffix(request.Path, "/"), "/")
	if len(parts) < 2 {
		return pushHTTPRoute{}, false
	}
	last := parts[len(parts)-1]
	base := ""
	discovery := false
	valid := false
	if strings.EqualFold(last, "git-receive-pack") {
		base = strings.Join(parts[:len(parts)-1], "/")
		valid = last == "git-receive-pack" && !strings.HasSuffix(request.Path, "/") && request.Method == "POST" && request.query == "" && !request.forceQuery
	} else if len(parts) >= 3 && strings.EqualFold(last, "refs") && strings.EqualFold(parts[len(parts)-2], "info") {
		query, err := url.ParseQuery(request.query)
		wanted := false
		for key, values := range query {
			if strings.EqualFold(key, "service") {
				for _, value := range values {
					wanted = wanted || value == "git-receive-pack"
				}
			}
		}
		if !wanted {
			return pushHTTPRoute{}, false
		}
		base = strings.Join(parts[:len(parts)-2], "/")
		discovery = true
		values := query["service"]
		valid = last == "refs" && parts[len(parts)-2] == "info" && !strings.HasSuffix(request.Path, "/") && err == nil && request.Method == "GET" && len(query) == 1 && len(values) == 1 && values[0] == "git-receive-pack" && request.query == "service=git-receive-pack"
	} else {
		return pushHTTPRoute{}, false
	}
	repo, err := CanonicalPushTarget(request.Host + strings.TrimSuffix(base, ".git"))
	valid = valid && err == nil && request.Port == 443 && request.scheme == "https"
	return pushHTTPRoute{repo: repo, discovery: discovery, valid: valid}, true
}

// A typed push grant supplies networking only for these protocol requests.
// It never overrides a matched net deny. Discovery is a fresh read-only
// availability witness; the command guard reserves the POST's push slot.
func (e *Engine) pushHTTPAllowanceLocked(request Request, permit *Permit, mode evalMode) Decision {
	route, found := gitPushHTTPRoute(request)
	if !found || !route.valid {
		return Decision{Rule: "push:route"}
	}
	if permit != nil && !route.discovery {
		if owned := e.evaluatePushLocked(route.repo, permit, evalRevalidate, request.Now); owned.Allow {
			return owned
		}
	}
	return e.evaluatePushLocked(route.repo, nil, evalPrecheck, request.Now)
}

func (e *Engine) pushHTTPWitnesses(host string, port int, at time.Time) []Request {
	if port != 443 {
		return nil
	}
	var requests []Request
	for _, layer := range e.layers {
		for _, rule := range layer.rules {
			if compiledDoor(rule) != api.DoorPush || rule.host != host || !rule.until.IsZero() && !at.Before(rule.until) {
				continue
			}
			base := rule.path + ".git"
			requests = append(requests, Request{Host: host, Port: port, Method: "GET", Path: base + "/info/refs", query: "service=git-receive-pack", scheme: "https", Now: at}, Request{Host: host, Port: port, Method: "POST", Path: base + "/git-receive-pack", scheme: "https", Now: at})
		}
	}
	return requests
}

func (e *Engine) hasPushInspectionHost(host string, port int) bool {
	return len(e.pushHTTPWitnesses(host, port, e.now())) != 0
}
