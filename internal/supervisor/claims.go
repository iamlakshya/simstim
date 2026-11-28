package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	// MaxCtlEventBytes caps each ctl TEvent payload from the guest (spec 5.3 & 10.3).
	MaxCtlEventBytes = 64 << 10
	// MaxClaimPorts caps the number of ports accepted in one guest claim (spec 10.3).
	MaxClaimPorts = 64
	// MaxCtlEventsPerSec caps the sustained rate of ctl events from the guest (spec 10.3).
	MaxCtlEventsPerSec = 20

	maxReasonRunes = 200
	maxDetailRunes = 500
	maxSignalRunes = 32
	maxAgentIDLen  = 256
)

var (
	sourcePattern    = regexp.MustCompile(`^[a-z0-9:._-]{1,64}$`)
	errorCodePattern = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
)

// cleanGuestError cleans an untrusted proto.Error returned by the guest so no
// control characters, bidi overrides, invalid codes or raw Data fields cross
// the wall (spec 5.3).
func cleanGuestError(e *proto.Error, fallbackCode, fallbackMsg, fallbackHint string) *proto.Error {
	if e == nil {
		return &proto.Error{Code: fallbackCode, Message: fallbackMsg, Hint: fallbackHint}
	}
	code := e.Code
	if !errorCodePattern.MatchString(code) {
		code = fallbackCode
	}
	msg := api.CleanText(e.Message, maxDetailRunes)
	if msg == "" {
		msg = fallbackMsg
	}
	hint := api.CleanText(e.Hint, maxReasonRunes)
	if hint == "" {
		hint = fallbackHint
	}
	return &proto.Error{Code: code, Message: msg, Hint: hint}
}

// ParseEvent validates and cleans one ctl TEvent JSON payload from the guest.
// Every string kept from a guest claim is cleaned through api.CleanText.
func ParseEvent(raw []byte) (guest.Event, error) {
	if len(raw) == 0 {
		return guest.Event{}, errors.New("ctl event is empty")
	}
	if len(raw) > MaxCtlEventBytes {
		return guest.Event{}, fmt.Errorf("ctl event has %d bytes, over the %d-byte limit", len(raw), MaxCtlEventBytes)
	}
	var ev guest.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return guest.Event{}, fmt.Errorf("decode ctl event: %w", err)
	}
	ev.Session = api.CleanText(ev.Session, 64)
	switch ev.Type {
	case "status":
		switch ev.State {
		case api.StateWorking, api.StateIdle, api.StateNeedsInput:
		default:
			return guest.Event{}, fmt.Errorf("invalid claimed status state %q", api.CleanText(ev.State, 32))
		}
		if ev.Source == "" {
			ev.Source = "guest"
		} else if !sourcePattern.MatchString(ev.Source) {
			return guest.Event{}, fmt.Errorf("invalid claimed status source %q", api.CleanText(ev.Source, 64))
		}
		ev.Reason = api.CleanText(ev.Reason, maxReasonRunes)
		return ev, nil

	case "alert":
		switch ev.Kind {
		case alerts.NeedsInput, alerts.TurnDone, alerts.Memory:
		default:
			return guest.Event{}, fmt.Errorf("invalid guest alert kind %q", api.CleanText(ev.Kind, 32))
		}
		ev.Detail = api.CleanText(ev.Detail, maxDetailRunes)
		if ev.TurnNS < 0 {
			ev.TurnNS = 0
		}
		return ev, nil

	case "agent_id":
		if ev.AgentID == "" || len(ev.AgentID) > maxAgentIDLen {
			return guest.Event{}, errors.New("invalid agent_id length")
		}
		ev.IDSource = api.CleanText(ev.IDSource, 64)
		return ev, nil

	case "ports":
		ev.Ports = cleanPorts(ev.Ports)
		return ev, nil

	case "ended":
		ev.Signal = api.CleanText(ev.Signal, maxSignalRunes)
		return ev, nil

	case "resize":
		if ev.Cols <= 0 || ev.Rows <= 0 {
			return guest.Event{}, errors.New("invalid resize dimensions")
		}
		ev.Cols = min(ev.Cols, 4096)
		ev.Rows = min(ev.Rows, 2048)
		return ev, nil

	case "heartbeat", "activity", "refs", "mem":
		return ev, nil

	default:
		return guest.Event{}, fmt.Errorf("unknown ctl event type %q", api.CleanText(ev.Type, 32))
	}
}

func cleanPorts(in []api.Port) []api.Port {
	if len(in) == 0 {
		return nil
	}
	if len(in) > MaxClaimPorts {
		in = in[:MaxClaimPorts]
	}
	out := make([]api.Port, 0, len(in))
	for _, p := range in {
		if p.Port <= 0 || p.Port > 65535 {
			continue
		}
		out = append(out, api.Port{
			Port:    p.Port,
			Address: api.CleanText(p.Address, 64),
			PID:     max(p.PID, 0),
			Process: api.CleanText(p.Process, 64),
		})
	}
	return out
}

// eventLimiter is a simple token bucket capping guest ctl events at rate/s.
type eventLimiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newEventLimiter(perSec int) *eventLimiter {
	f := float64(perSec)
	return &eventLimiter{rate: f, burst: f, tokens: f, last: time.Now()}
}

func (l *eventLimiter) Allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if elapsed := now.Sub(l.last).Seconds(); elapsed > 0 {
		l.tokens = min(l.burst, l.tokens+elapsed*l.rate)
		l.last = now
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
