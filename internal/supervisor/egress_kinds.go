package supervisor

import (
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

// setPublicInitial stores only exact trusted initial metadata before its first
// write. It is a finite wire-size projection, never a second policy engine.
func (c *egressCtl) setPublicInitial(args egress.InitArgs) {
	args.Decoys=nil // Only the private applied engine retains generated values.
	args.Secrets = nil
	args.Policy = api.ClonePolicy(args.Policy)
	args.AgentKinds = slices.Clone(args.AgentKinds)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publicInit, c.publicInitReady = args, true
	if c.acceptedKinds == nil {
		c.acceptedKinds = make(map[string]bool)
	}
	for _, kind := range args.AgentKinds {
		if row, known := agents.Find(kind); known {
			c.acceptedKinds[row.Kind] = true
		}
	}
	if row, known := agents.Find(args.Agent); known {
		c.acceptedKinds[row.Kind] = true
	}
}

// beginPublicKind covers event-before-member-ACK sizing. Only an actual
// validated trusted request supplies kind. Known rejection prunes pending;
// success or uncertain owned-link closure retains conservative public metadata.
func (c *egressCtl) beginPublicKind(kind string) func(retain bool) {
	row, known := agents.Find(kind)
	if !known {
		return func(bool) {} // custom/empty means no built-in kind addition
	}
	kind = row.Kind
	c.mu.Lock()
	if c.pendingKinds == nil {
		c.pendingKinds = make(map[string]int)
	}
	c.pendingKinds[kind]++
	c.mu.Unlock()
	var once sync.Once
	return func(retain bool) {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.pendingKinds[kind]--
			if c.pendingKinds[kind] == 0 {
				delete(c.pendingKinds, kind)
			}
			if retain {
				if c.acceptedKinds == nil {
					c.acceptedKinds = make(map[string]bool)
				}
				c.acceptedKinds[kind] = true
			}
		})
	}
}

func (c *egressCtl) publicKindsLocked() []string {
	out := []string{}
	for kind := range c.acceptedKinds {
		out = append(out, kind)
	}
	for kind := range c.pendingKinds {
		if !c.acceptedKinds[kind] {
			out = append(out, kind)
		}
	}
	slices.Sort(out)
	return out
}

// publicInitial returns exact trusted initial identity/DNS plus current
// protected policy and accepted+in-flight kind projection. No disk/RPC runs
// under the existing c.mu, and no guest kind or all-agent guess is consulted.
func (c *egressCtl) publicInitial(snapshot host.Manifest) (egress.InitArgs, error) {
	c.mu.Lock()
	if !c.publicInitReady {
		c.mu.Unlock()
		return egress.InitArgs{}, execRefusal("the owned egress has no initial public metadata")
	}
	args := c.publicInit
	args.AgentKinds = c.publicKindsLocked()
	c.mu.Unlock()
	args.Policy = api.ClonePolicy(args.Policy)
	if snapshot.Policy != nil {
		args.Policy = api.ClonePolicy(*snapshot.Policy)
	}
	args.Secrets = nil
	return args, nil
}
