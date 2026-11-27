package supervisor

import (
	"encoding/json"
	"fmt"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (c *egressCtl) policyTransaction(op string, raw []byte) func(bool, error) {
	if op != egress.CtlInit && op != egress.CtlBoundInit && op != egress.CtlPolicyUpdate && op != egress.CtlMemberAdd {
		return func(bool, error) {}
	}
	c.policyMu.Lock()
	finish := c.events.preparePolicy(op, raw)
	return func(confirmed bool, resultErr error) {
		defer c.policyMu.Unlock()
		if !confirmed {
			// A missing reply cannot leave an unrecorded policy running.
			c.closeOwnedControl()
		}
		finish(resultErr == nil)
	}
}

func (c *egressCtl) closeOwnedControl() {
	if c.owner != nil && c.owner.sess != nil {
		_ = c.owner.sess.Close()
	} else if c.st != nil {
		_ = c.st.Close()
	}
}

func egressReply(op string, resp proto.Response, out any) error {
	if !resp.OK {
		if resp.Error != nil {
			return resp.Error
		}
		return fmt.Errorf("egress %s failed", op)
	}
	if out != nil && len(resp.Result) != 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("decode egress %s reply: %w", op, err)
		}
	}
	return nil
}
