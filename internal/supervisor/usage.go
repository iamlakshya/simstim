package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// handleEgressUsageAndBudget folds model token usage from egress audit events
// into the session's trusted manifest (copy-on-write so shallow manifest copies
// never race) and raises the model_budget alert once per configured token cap
// (isolation spec 17.8 and 10.8).
func (s *Supervisor) handleEgressUsageAndBudget(ev egress.AuditEvent) {
	if ev.Usage == nil && ev.Rule != "model_budget" {
		return
	}
	var saveSnap *host.Manifest
	s.mu.Lock()
	if ev.Usage != nil {
		next := s.m.Usage.Clone()
		if next == nil {
			next = &api.SessionUsage{}
		}
		if next.ByModel == nil {
			next.ByModel = map[string]api.TokenUsage{}
		}
		api.AddUsage(&next.Total, *ev.Usage)
		model := ev.Usage.Model
		if model == "" {
			model = "unknown"
		}
		cur := next.ByModel[model]
		cur.Model = model
		api.AddUsage(&cur, *ev.Usage)
		next.ByModel[model] = cur
		if ev.BudgetLimit > 0 {
			next.Budget = ev.BudgetLimit
		} else if s.m.Wall != nil && s.m.Wall.ModelBudget > 0 {
			next.Budget = s.m.Wall.ModelBudget
		}
		s.m.Usage = next
		cp := *s.m
		saveSnap = &cp
	}
	if ev.Rule == "model_budget" {
		if ev.BudgetLimit > 0 {
			next := s.m.Usage.Clone()
			if next == nil {
				next = &api.SessionUsage{}
			}
			next.Budget = ev.BudgetLimit
			s.m.Usage = next
		}
		if !s.budgetAlerted || (ev.BudgetLimit > 0 && ev.BudgetLimit != s.budgetAlertLimit) {
			s.budgetAlerted = true
			s.budgetAlertLimit = ev.BudgetLimit
			s.alertModelBudget(ev.BudgetUsed, ev.BudgetLimit)
		}
	}
	s.mu.Unlock()

	if saveSnap != nil {
		if err := s.save(saveSnap, false); err != nil {
			s.log.Warn("manifest usage not saved", "err", err)
		}
	}
}

// alertModelBudget sends the model_budget alert. Caller holds s.mu.
func (s *Supervisor) alertModelBudget(used, limit int64) {
	if s.alerts == nil {
		return
	}
	ev := s.makeAlert(alerts.ModelBudget)
	if limit > 0 {
		ev.Usage = fmt.Sprintf("%d of %d tokens", used, limit)
	}
	s.alerts.Notify(ev)
}

func (s *Supervisor) handleUsage(w *proto.Writer, req proto.Request) {
	if s.egress != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		summary, err := s.egress.Usage(ctx)
		cancel()
		if err == nil {
			res, _ := json.Marshal(summary)
			_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
			return
		}
	}
	s.mu.Lock()
	var u api.SessionUsage
	if s.m.Usage != nil {
		u = *s.m.Usage.Clone()
	}
	s.mu.Unlock()
	res, _ := json.Marshal(u)
	_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: res})
}

func (s *Supervisor) handlePolicyReload(w *proto.Writer, req proto.Request) {
	fail := func(code, msg, hint string) {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: code, Message: msg, Hint: hint}})
	}
	if s.egress == nil {
		fail("no_egress", "this session has no egress process", "Start a walled session with network egress enabled.")
		return
	}
	var args egress.ReloadArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		fail("bad_request", "invalid policy reload request", "Pass valid policy_toml JSON.")
		return
	}
	cfg, err := egress.ParsePolicy([]byte(args.PolicyTOML))
	if err != nil {
		fail("bad_policy", err.Error(), "Fix the session policy TOML.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = s.egress.Reload(ctx, args.PolicyTOML)
	cancel()
	if err != nil {
		fail("bad_policy", err.Error(), "Check the session policy fields.")
		return
	}
	s.mu.Lock()
	if s.m.Wall != nil {
		wallCopy := *s.m.Wall
		wallCopy.PolicyTOML = args.PolicyTOML
		wallCopy.ModelBudget = cfg.Network.ModelBudget
		wallCopy.UploadBudget = cfg.Network.UploadBudget
		s.m.Wall = &wallCopy
	}
	if cfg.Network.ModelBudget > 0 || s.m.Usage != nil {
		next := s.m.Usage.Clone()
		if next == nil {
			next = &api.SessionUsage{}
		}
		next.Budget = cfg.Network.ModelBudget
		s.m.Usage = next
		if cfg.Network.ModelBudget == 0 || next.Total.TotalTokens < cfg.Network.ModelBudget {
			s.budgetAlerted = false
			s.budgetAlertLimit = 0
		}
	}
	cp := *s.m
	s.mu.Unlock()
	_ = s.save(&cp, false)
	_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true})
}
