package guest

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// filesystemFreezeV1 belongs to the mount creator. Its syscall closure retains
// that exact mount descriptor across pivot; a request supplies no path or fd.
type filesystemFreezeV1 struct {
	mu             sync.Mutex
	change         func(bool) error
	timer          *time.Timer
	generation     uint64
	frozen, closed bool
	lastError      error
}

func freezeErrorV1(message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &proto.Error{Code: "freeze", Message: message, Hint: "Retry the operation on the original running sandbox; inspect its log if it fails again."}
}

func (f *filesystemFreezeV1) renew(maxMS int) error {
	if f == nil || maxMS < 1 || maxMS > proto.MaxFreezeMS {
		return freezeErrorV1("the freeze lease must last between 1 and 5000 milliseconds", nil)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.change == nil {
		return freezeErrorV1("the original session filesystem is unavailable", nil)
	}
	if f.lastError != nil {
		return freezeErrorV1("the original session filesystem did not thaw", f.lastError)
	}
	if !f.frozen {
		if err := f.change(true); err != nil {
			return freezeErrorV1("cannot freeze the original session filesystem", err)
		}
		f.frozen = true
	}
	f.generation++
	generation := f.generation
	if f.timer != nil {
		f.timer.Stop()
	}
	f.timer = time.AfterFunc(time.Duration(maxMS)*time.Millisecond, func() { f.expire(generation) })
	return nil
}

func (f *filesystemFreezeV1) expire(generation uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if generation != f.generation || f.closed || !f.frozen {
		return
	}
	f.lastError = f.change(false)
	if f.lastError == nil {
		f.frozen = false
		f.timer = nil
		return
	}
	// A failed kernel thaw is never reported as completion. Keep its original
	// descriptor and retry, including when the supervisor's link has vanished.
	f.timer = time.AfterFunc(100*time.Millisecond, func() { f.expire(generation) })
}

func (f *filesystemFreezeV1) thaw() error {
	if f == nil {
		return freezeErrorV1("the original session filesystem is unavailable", nil)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.thawLocked()
}

func (f *filesystemFreezeV1) thawLocked() error {
	if !f.frozen {
		return nil
	}
	if f.change == nil {
		return freezeErrorV1("the original mount has no thaw operation", nil)
	}
	if err := f.change(false); err != nil {
		f.lastError = err
		return freezeErrorV1("cannot thaw the original session filesystem", err)
	}
	f.generation++
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.frozen, f.lastError = false, nil
	return nil
}

func (f *filesystemFreezeV1) close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.thawLocked(); err != nil {
		return err
	}
	f.closed = true
	return nil
}

type guestFreezeOwnerV1 interface {
	sessionFreezeV1() (*filesystemFreezeV1, error)
}

func (g *Init) freezeControlV1(req proto.Request, reply func(any, error)) bool {
	if req.Op != proto.CtlFreeze && req.Op != proto.CtlThaw {
		return false
	}
	owner, ok := g.vm.(guestFreezeOwnerV1)
	if !ok {
		reply(nil, &proto.Error{Code: "freeze_off", Message: "this guest has no original microvm session filesystem", Hint: "Use the process wall's outside cgroup freezer or a microvm."})
		return true
	}
	f, err := owner.sessionFreezeV1()
	if err == nil {
		if req.Op == proto.CtlFreeze {
			var args proto.CtlFreezeArgs
			if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil {
				err = freezeErrorV1("the freeze request has invalid fields", nil)
			} else {
				err = f.renew(args.MaxMS)
			}
		} else if !api.ExactJSONFields(req.Args, struct{}{}) {
			err = freezeErrorV1("thaw takes an empty object", nil)
		} else {
			err = f.thaw()
		}
	}
	reply(struct{}{}, err)
	return true
}
