package client

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const policyHostParallel = 8
const policyHostWait = 5 * time.Second

// PolicyChange holds the common cross-process order over file writes and sends.
type PolicyChange struct {
	mu      sync.Mutex
	root    *os.Root
	anchors []stateRootAnchor
	lock    *stateFileLock
	release func()
}
type policyChangeKey struct{}

func BeginPolicyChange(ctx context.Context) (*PolicyChange, error) {
	release, err := lockStateProcess(ctx, stateProcessMutex(nil, "policy"))
	if err != nil {
		return nil, err
	}
	root, anchors, err := openStateDirRootContext(ctx, nil, "policy", true)
	if err != nil {
		release()
		return nil, err
	}
	held, err := acquireStateFileLock(ctx, root, "policy", true)
	if err != nil {
		root.Close()
		release()
		return nil, err
	}
	return &PolicyChange{root: root, anchors: anchors, lock: held, release: release}, nil
}
func (p *PolicyChange) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.root == nil {
		return nil
	}
	err := p.lock.closeOwned(context.Background())
	err = errors.Join(err, p.root.Close())
	p.release()
	p.root = nil
	return err
}
func (p *PolicyChange) Context(ctx context.Context) context.Context {
	owned := context.WithValue(ctx, policyChangeKey{}, p)
	previous, _ := ctx.Value(stateAuthorityKey{}).(func() error)
	return context.WithValue(owned, stateAuthorityKey{}, func() error {
		if previous != nil {
			if err := previous(); err != nil {
				return err
			}
		}
		return checkPolicyChange(owned)
	})
}
func checkPolicyChange(ctx context.Context) error {
	p, _ := ctx.Value(policyChangeKey{}).(*PolicyChange)
	if p == nil {
		return policyReplaceSourceError()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.root == nil || p.lock == nil {
		return policyReplaceSourceError()
	}
	if err := stateRootAnchorsMatch(ctx, p.root, p.anchors, "policy"); err != nil {
		return err
	}
	return p.lock.check(ctx, p.root)
}

// PolicyApplication owns one planned session on its original accepted Conn.
type PolicyApplication struct {
	Host        string
	Replacement *PolicyReplacement
}

type policyHostPlan struct {
	host         Host
	conn         *Conn
	applications []PolicyApplication
	failure      error
}

// PolicyPropagation keeps all connections alive through preview and questions.
type PolicyPropagation struct{ hosts []*policyHostPlan }
type policyPropagationContactKey struct{}

// WithoutPolicyReplay preserves a read-only or dry-run contact. It grants no
// action and prevents an earlier queued policy change from running there.
func WithoutPolicyReplay(ctx context.Context) context.Context {
	return context.WithValue(ctx, policyPropagationContactKey{}, true)
}

// PolicySessionChange is printed only after an exact acknowledged source hash.
type PolicySessionChange struct {
	Host      string   `json:"host"`
	Session   string   `json:"session"`
	State     string   `json:"state"`
	Hash      string   `json:"hash,omitempty"`
	NextStart []string `json:"next_start,omitempty"`
	Error     string   `json:"error,omitempty"`
}
type PolicyPropagationResult struct {
	Sessions  []PolicySessionChange `json:"sessions"`
	Unreached []string              `json:"unreached"`
}

// PlanPolicyPropagation contacts saved hosts in bounded parallel groups. A
// local pin decides which sections reach a sandbox; remote labels cannot.
func PlanPolicyPropagation(ctx context.Context, layer *FileLayer) (*PolicyPropagation, error) {
	if _, err := ReadPolicySettings(); err != nil {
		return nil, err
	}
	hosts, err := LoadHosts()
	if err != nil {
		return nil, err
	}
	if len(hosts) > 1024 {
		return nil, &UserError{Msg: "there are too many hosts for one policy apply", Hint: "Remove unused saved hosts before applying policy.", Code: "policy_hosts", Kind: KindRefused}
	}
	plan := &PolicyPropagation{hosts: make([]*policyHostPlan, len(hosts))}
	sem := make(chan struct{}, policyHostParallel)
	var wg sync.WaitGroup
	for index, host := range hosts {
		wg.Add(1)
		go func(index int, host Host) {
			defer wg.Done()
			entry := &policyHostPlan{host: host}
			plan.hosts[index] = entry
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				entry.failure = ctx.Err()
				return
			}
			defer func() { <-sem }()
			attempt, cancel := context.WithTimeout(ctx, policyHostWait)
			defer cancel()
			conn, err := Dial(WithoutPolicyReplay(attempt), host, false)
			if err != nil {
				entry.failure = err
				return
			}
			entry.conn = conn
			entry.applications, entry.failure = planPolicyHost(attempt, ctx, conn, layer)
		}(index, host)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		_ = plan.Close()
		return nil, err
	}
	return plan, nil
}

func planPolicyHost(attempt, lifetime context.Context, c *Conn, layer *FileLayer) ([]PolicyApplication, error) {
	var rows []Session
	if err := c.Call(attempt, "ls", nil, &rows); err != nil {
		return nil, err
	}
	rows = ValidSessions(c.Host.Name, rows)
	var applications []PolicyApplication
	for _, row := range rows {
		if !row.Alive || row.Wall == nil || row.SandboxOf != "" {
			continue
		}
		request := NewPolicyReplaceGrantRequest(row.ID)
		requestCtx := WithGrantRequest(lifetime, request)
		operationCtx, cancel := context.WithTimeout(requestCtx, policyHostWait)
		if deadline, ok := attempt.Deadline(); ok {
			cancel()
			operationCtx, cancel = context.WithDeadline(requestCtx, deadline)
		}
		replacement, err := c.PlanPolicyReplacement(operationCtx, row.ID)
		cancel()
		if err != nil {
			_ = request.Close()
			for _, a := range applications {
				_ = a.Replacement.Close()
			}
			return nil, err
		}
		if layer != nil && !policyPinSelectsLayer(replacement.operation.creation, *layer) {
			_ = replacement.Close()
			continue
		}
		applications = append(applications, PolicyApplication{Host: c.Host.Name, Replacement: replacement})
	}
	return applications, nil
}

func policyPinSelectsLayer(creation *SandboxPolicy, layer FileLayer) bool {
	if creation == nil {
		return false
	}
	pin, err := api.DecodePolicySourcePin(creation.PolicySources)
	if err != nil {
		return false
	}
	for _, ref := range pin.Layers {
		if ref.Source == "" && ref.Kind == layer.Kind && ref.Name == layer.Name {
			return true
		}
	}
	return false
}

func (p *PolicyPropagation) Applications() []PolicyApplication {
	if p == nil {
		return nil
	}
	var out []PolicyApplication
	for _, host := range p.hosts {
		out = append(out, host.applications...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host == out[j].Host {
			return out[i].Replacement.Preview().Session < out[j].Replacement.Preview().Session
		}
		return out[i].Host < out[j].Host
	})
	return out
}

func (p *PolicyPropagation) Unreached() []string {
	var names []string
	if p != nil {
		for _, host := range p.hosts {
			if host.failure != nil {
				names = append(names, host.host.Name)
			}
		}
	}
	slices.Sort(names)
	return names
}

// Block records a failed original local gate before any parallel send.
func (p *PolicyPropagation) Block(name string, err error) {
	if p == nil || err == nil {
		return
	}
	for _, host := range p.hosts {
		if host.host.Name == name {
			host.failure = err
		}
	}
}

func (p *PolicyPropagation) Close() error {
	if p == nil {
		return nil
	}
	var result error
	for _, host := range p.hosts {
		for _, a := range host.applications {
			result = errors.Join(result, a.Replacement.Close())
		}
		if host.conn != nil {
			host.conn.Close()
		}
	}
	p.hosts = nil
	return result
}

// Apply runs after the front end has asked its serial original-R questions.
// It waits at most five seconds for each host, including all its sandboxes.
func (p *PolicyPropagation) Apply(ctx context.Context) (PolicyPropagationResult, error) {
	result := PolicyPropagationResult{Sessions: []PolicySessionChange{}, Unreached: []string{}}
	if p == nil {
		return result, policyReplaceSourceError()
	}
	if err := checkPolicyChange(ctx); err != nil {
		return result, err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, policyHostParallel)
	var reached []string
	for _, host := range p.hosts {
		wg.Add(1)
		go func(host *policyHostPlan) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				result.Unreached = append(result.Unreached, host.host.Name)
				mu.Unlock()
				return
			}
			defer func() { <-sem }()
			attempt, cancel := context.WithTimeout(ctx, policyHostWait)
			defer cancel()
			failed := host.failure != nil
			var changes []PolicySessionChange
			if !failed {
				for _, a := range host.applications {
					preview := a.Replacement.Preview()
					if !preview.Changed {
						continue
					}
					ack, err := a.Replacement.Apply(a.Replacement.Context(attempt))
					change := PolicySessionChange{Host: a.Host, Session: preview.Session}
					if err != nil {
						failed = true
						change.State = "unreached"
						change.Error = CleanLine(err.Error())
					} else {
						change.State, change.Hash, change.NextStart = ack.State, ack.Hash, slices.Clone(ack.NextStart)
					}
					changes = append(changes, change)
				}
			}
			mu.Lock()
			result.Sessions = append(result.Sessions, changes...)
			if failed {
				result.Unreached = append(result.Unreached, host.host.Name)
			} else {
				reached = append(reached, host.host.Name)
			}
			mu.Unlock()
		}(host)
	}
	wg.Wait()
	slices.Sort(result.Unreached)
	sort.Slice(result.Sessions, func(i, j int) bool {
		if result.Sessions[i].Host == result.Sessions[j].Host {
			return result.Sessions[i].Session < result.Sessions[j].Session
		}
		return result.Sessions[i].Host < result.Sessions[j].Host
	})
	if err := updatePolicyPending(ctx, reached, result.Unreached); err != nil {
		return result, err
	}
	return result, ctx.Err()
}

// ReplayPendingPolicy is read-free for an unrecorded host. It creates fresh
// actions only from current local pins on this actual accepted connection.
// A permission question keeps the host pending; it never answers itself.
func ReplayPendingPolicy(ctx context.Context, c *Conn) error {
	if skip, _ := ctx.Value(policyPropagationContactKey{}).(bool); skip {
		return nil
	}
	if c == nil {
		return nil
	}
	state, err := readPolicyPending(ctx)
	if err != nil {
		return err
	}
	lockdown, err := lockdownContactRecordedV1(ctx, c, state)
	if err != nil {
		return err
	}
	if !lockdown && !slices.Contains(state.Hosts, c.Host.Name) {
		return nil
	}
	change, err := BeginPolicyChange(ctx)
	if err != nil {
		return err
	}
	defer change.Close()
	ctx = change.Context(ctx)
	// A newer command may have superseded pending intent while this waited.
	state, err = readPolicyPending(ctx)
	if err != nil {
		return err
	}
	attempt, cancel := context.WithTimeout(ctx, policyHostWait)
	defer cancel()
	if err := replayLockdownContactV1(attempt, c, state); err != nil {
		return err
	}
	if !slices.Contains(state.Hosts, c.Host.Name) {
		return nil
	}
	applications, err := planPolicyHost(attempt, ctx, c, nil)
	if err != nil {
		return err
	}
	defer func() {
		for _, a := range applications {
			_ = a.Replacement.Close()
		}
	}()
	for _, a := range applications {
		if !a.Replacement.Preview().Changed {
			continue
		}
		if err := a.Replacement.Check(a.Replacement.Context(attempt)); err != nil {
			return err
		}
		if _, err := a.Replacement.Apply(a.Replacement.Context(attempt)); err != nil {
			return err
		}
	}
	return updatePolicyPending(ctx, []string{c.Host.Name}, nil)
}
