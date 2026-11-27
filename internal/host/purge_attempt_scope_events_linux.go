//go:build linux

package host

import (
	"context"
	"errors"
	"sync"

	"github.com/godbus/dbus/v5"
)

// This one original scope has at most its start job and its own final empty
// stop job. Signal DATA is useful only on its retained native manager call.
type purgeAttemptScopeEventsV3 struct {
	mu      sync.Mutex
	unit    string
	path    dbus.ObjectPath
	removed bool
	jobs    map[dbus.ObjectPath]*purgeAttemptScopeJobV3
	err     error
	wake    chan struct{}
}

type purgeAttemptScopeJobV3 struct {
	path        dbus.ObjectPath
	id          uint32
	seen, ended bool
	result      string
}

func (events *purgeAttemptScopeEventsV3) observeV3(signal *dbus.Signal) {
	if signal == nil || signal.Path != systemdManagerPath {
		return
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	fault := func() {
		events.err = placementError("the original purge scope manager reported another or ambiguous generation")
	}
	switch signal.Name {
	case systemdManagerInterface + ".UnitNew", systemdManagerInterface + ".UnitRemoved":
		if len(signal.Body) != 2 {
			fault()
			break
		}
		unit, ok := signal.Body[0].(string)
		if !ok {
			fault()
			break
		}
		if unit != events.unit {
			return
		}
		path, ok := signal.Body[1].(dbus.ObjectPath)
		if !ok || !path.IsValid() || path == "/" {
			fault()
			break
		}
		if signal.Name == systemdManagerInterface+".UnitNew" {
			if events.path != "" || events.removed {
				fault()
				break
			}
			events.path = path
		} else {
			if events.path == "" || path != events.path || events.removed {
				fault()
				break
			}
			events.removed = true
		}
	case systemdManagerInterface + ".JobNew", systemdManagerInterface + ".JobRemoved":
		if len(signal.Body) < 3 {
			fault()
			break
		}
		unit, ok := signal.Body[2].(string)
		if !ok {
			fault()
			break
		}
		if unit != events.unit {
			return
		}
		id, idOK := signal.Body[0].(uint32)
		path, pathOK := signal.Body[1].(dbus.ObjectPath)
		if !idOK || id == 0 || !pathOK || !path.IsValid() || path == "/" {
			fault()
			break
		}
		job := events.jobs[path]
		if job == nil {
			if len(events.jobs) >= 2 {
				fault()
				break
			}
			job = &purgeAttemptScopeJobV3{path: path, id: id}
			events.jobs[path] = job
		}
		if job.id != 0 && job.id != id {
			fault()
			break
		}
		job.id = id
		if signal.Name == systemdManagerInterface+".JobNew" {
			if len(signal.Body) != 3 || job.seen {
				fault()
				break
			}
			job.seen = true
		} else {
			if len(signal.Body) != 4 || job.ended {
				fault()
				break
			}
			result, ok := signal.Body[3].(string)
			if !ok || result == "" {
				fault()
				break
			}
			job.ended, job.result = true, result
		}
	default:
		return
	}
	select {
	case events.wake <- struct{}{}:
	default:
	}
}

func (events *purgeAttemptScopeEventsV3) waitOriginalJobV3(ctx context.Context, path dbus.ObjectPath) (*purgeAttemptScopeJobV3, error) {
	if ctx == nil || !path.IsValid() || path == "/" {
		return nil, placementError("the original scope call returned no exact manager job")
	}
	for {
		events.mu.Lock()
		if events.err != nil {
			err := events.err
			events.mu.Unlock()
			return nil, err
		}
		job := events.jobs[path]
		if job != nil && job.seen && job.ended && events.path != "" {
			events.mu.Unlock()
			return job, nil
		}
		events.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-events.wake:
		}
	}
}

func (events *purgeAttemptScopeEventsV3) checkOriginalJobV3(job *purgeAttemptScopeJobV3) error {
	events.mu.Lock()
	defer events.mu.Unlock()
	if events.err != nil {
		return events.err
	}
	if job == nil || events.jobs[job.path] != job || !job.seen || !job.ended || job.id == 0 || events.path == "" {
		return placementError("the scope has no same original completed native manager job")
	}
	return nil
}

func (events *purgeAttemptScopeEventsV3) failureV3() error {
	events.mu.Lock()
	defer events.mu.Unlock()
	return errors.Join(events.err)
}

func (events *purgeAttemptScopeEventsV3) checkOnlyOriginalJobsV3(start, stop *purgeAttemptScopeJobV3) error {
	events.mu.Lock()
	defer events.mu.Unlock()
	if events.err != nil {
		return events.err
	}
	for _, job := range events.jobs {
		if job != start && job != stop {
			return placementError("another manager job changed the original purge scope")
		}
	}
	return nil
}
