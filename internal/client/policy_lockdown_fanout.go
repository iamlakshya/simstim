package client

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

type PolicyLockdownHostReportV1 struct {
	Host   string                   `json:"host"`
	Result api.HostLockdownResultV1 `json:"result"`
	Error  string                   `json:"error,omitempty"`
}
type PolicyLockdownReportV1 struct {
	Version   int                          `json:"v"`
	On        bool                         `json:"on"`
	Hosts     []PolicyLockdownHostReportV1 `json:"hosts"`
	Unreached []string                     `json:"unreached"`
}

// ChangeHostLockdownV1 applies only an explicit restriction toggle. It uses
// existing policy.lock ordering and never reads or propagates laptop layers.
func ChangeHostLockdownV1(ctx context.Context, hosts []Host, on bool, literal string) (PolicyLockdownReportV1, error) {
	report := PolicyLockdownReportV1{Version: 1, On: on, Hosts: []PolicyLockdownHostReportV1{}, Unreached: []string{}}
	if err := validateLockdownLiteralV1(on, literal); err != nil {
		return report, err
	}
	if len(hosts) == 0 {
		return report, &UserError{Msg: "there are no saved hosts for lockdown", Hint: "Save a host before changing its lockdown setting.", Code: "no_hosts", Kind: KindNotFound}
	}
	if len(hosts) > 1024 {
		return report, &UserError{Msg: "there are too many saved hosts for one lockdown action", Hint: "Choose a narrower --host scope.", Code: "policy_hosts", Kind: KindRefused}
	}
	seen := map[string]bool{}
	for _, host := range hosts {
		if seen[host.Name] {
			return report, policyLockdownSourceErrorV1()
		}
		seen[host.Name] = true
		request, err := NewPolicyLockdownRequestV1(host, on, literal)
		if err != nil {
			return report, err
		}
		_ = request.Close()
	}
	change, err := BeginPolicyChange(ctx)
	if err != nil {
		return report, err
	}
	defer change.Close()
	ctx = change.Context(ctx)
	bindings := map[string]policyLockdownBindingV1{}
	pending := map[string]policyLockdownPendingV1{}
	var names []string
	for _, host := range hosts {
		binding, err := lockdownBindingForHostV1(host)
		if err != nil {
			return report, err
		}
		bindings[host.Name] = binding
		pending[host.Name] = policyLockdownPendingV1{On: on, Binding: binding}
		names = append(names, host.Name)
	}
	if on {
		if err := updateLocalLockdownV1(ctx, bindings, nil); err != nil {
			return report, err
		}
		if err := updateLockdownPendingV1(ctx, pending, nil); err != nil {
			return report, err
		}
	} else {
		// Record off before unlinking the local on mark. Replay retains this
		// order after a crash, so an interrupted cleanup cannot re-enable on.
		if err := updateLockdownPendingV1(ctx, pending, nil); err != nil {
			return report, err
		}
		if err := updateLocalLockdownV1(ctx, nil, names); err != nil {
			return report, err
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, policyHostParallel)
	success := map[string]policyLockdownBindingV1{}
	for _, host := range hosts {
		wg.Add(1)
		go func(host Host) {
			defer wg.Done()
			entry := PolicyLockdownHostReportV1{Host: host.Name}
			var actionErr error
			var hostID string
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				actionErr = ctx.Err()
			}
			if actionErr == nil {
				func() {
					defer func() { <-sem }()
					request, err := NewPolicyLockdownRequestV1(host, on, literal)
					if err != nil {
						actionErr = err
						return
					}
					defer request.Close()
					lifetime := request.Context(ctx)
					attempt, cancel := context.WithTimeout(lifetime, policyHostWait)
					defer cancel()
					attempt = WithoutPolicyReplay(WithTierQuestion(WithoutTierLoginPrompt(attempt), nil))
					conn, err := Dial(attempt, host, false)
					if err != nil {
						actionErr = err
						return
					}
					defer conn.Close()
					hostID = conn.HostID()
					if err := checkLockdownBindingV1(conn.Host, hostID, bindings[host.Name]); err != nil {
						actionErr = err
						return
					}
					entry.Result, actionErr = request.Apply(attempt, conn)
				}()
			}
			if actionErr != nil {
				entry.Error = CleanLine(policyLockdownClientErrorV1(actionErr).Error())
			}
			mu.Lock()
			defer mu.Unlock()
			report.Hosts = append(report.Hosts, entry)
			if actionErr != nil {
				report.Unreached = append(report.Unreached, host.Name)
			} else {
				binding := bindings[host.Name]
				binding.Pending, binding.HostID = false, hostID
				success[host.Name] = binding
			}
		}(host)
	}
	wg.Wait()
	var reached []string
	for name := range success {
		reached = append(reached, name)
	}
	slices.Sort(reached)
	if on && len(success) > 0 {
		if err := updateLocalLockdownV1(ctx, success, nil); err != nil {
			return report, err
		}
	}
	if err := updateLockdownPendingV1(ctx, nil, reached); err != nil {
		return report, err
	}
	slices.Sort(report.Unreached)
	sort.Slice(report.Hosts, func(i, j int) bool { return report.Hosts[i].Host < report.Hosts[j].Host })
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if len(report.Unreached) > 0 {
		data, _ := json.Marshal(report)
		return report, &UserError{Msg: "lockdown could not reach: " + strings.Join(report.Unreached, ", "), Hint: "Keep these hosts saved; the same recorded change will be retried at their next contact.", Code: "policy_unreached", Kind: KindFailed, Data: data}
	}
	return report, nil
}

func lockdownContactRecordedV1(ctx context.Context, c *Conn, state policyPendingState) (bool, error) {
	if c == nil {
		return false, nil
	}
	if _, exists := state.Lockdowns[c.Host.Name]; exists {
		return true, nil
	}
	active, err := readLocalLockdownV1(ctx)
	if err != nil {
		return false, err
	}
	binding, exists := active.Hosts[c.Host.Name]
	return exists && binding.Pending, nil
}
