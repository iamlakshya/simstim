//go:build linux

package host

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/godbus/dbus/v5"
)

type managerStartupEventsV1 struct {
	mu                 sync.Mutex
	wake               chan struct{}
	unit               string
	path               dbus.ObjectPath
	job                dbus.ObjectPath
	observedJob        dbus.ObjectPath
	jobID              uint32
	removedJob         dbus.ObjectPath
	removedID          uint32
	result             string
	invocation         string
	activation         unitActivationProperties
	err                error
	dispatched         bool
	negativeReply      bool
	unitRemoved        bool
	loaded             bool
	previousInvocation string
}

func (events *managerStartupEventsV1) observe(message *dbus.Message) {
	if message.Type != dbus.TypeSignal {
		return
	}
	iface, _ := message.Headers[dbus.FieldInterface].Value().(string)
	member, _ := message.Headers[dbus.FieldMember].Value().(string)
	path, _ := message.Headers[dbus.FieldPath].Value().(dbus.ObjectPath)
	events.mu.Lock()
	defer events.mu.Unlock()
	if events.unit == "" {
		return
	}
	changed := false
	fault := func() {
		events.err = sessionLeaseError("the original manager changed or ambiguously reported its first startup activation")
		changed = true
	}
	switch {
	case iface == systemdManagerInterface && (member == "JobNew" || member == "JobRemoved"):
		if len(message.Body) < 3 {
			fault()
			break
		}
		unit, ok := message.Body[2].(string)
		if !ok {
			fault()
			break
		}
		if unit != events.unit {
			return
		}
		id, idOK := message.Body[0].(uint32)
		job, jobOK := message.Body[1].(dbus.ObjectPath)
		if !idOK || !jobOK || id == 0 || !job.IsValid() || job == "/" {
			fault()
			break
		}
		changed = true
		if member == "JobNew" {
			if len(message.Body) != 3 || events.observedJob != "" || events.job != "" && events.job != job {
				fault()
				break
			}
			events.observedJob, events.jobID = job, id
		} else {
			if len(message.Body) != 4 {
				fault()
				break
			}
			result, resultOK := message.Body[3].(string)
			if !resultOK || events.removedJob != "" ||
				events.observedJob != "" && (events.observedJob != job || events.jobID != id) || events.job != "" && events.job != job {
				fault()
				break
			}
			events.removedJob, events.removedID, events.result = job, id, result
		}
	case iface == systemdManagerInterface && (member == "UnitNew" || member == "UnitRemoved"):
		if len(message.Body) != 2 {
			fault()
			break
		}
		unit, ok := message.Body[0].(string)
		if !ok || unit != events.unit {
			return
		}
		unitPath, ok := message.Body[1].(dbus.ObjectPath)
		if !ok || !unitPath.IsValid() {
			fault()
			break
		}
		if member == "UnitRemoved" {
			if events.path != "" && events.path == unitPath {
				events.unitRemoved = true
			}
			fault()
			break
		}
		if events.path != "" {
			if events.loaded && events.path == unitPath {
				return
			}
			fault()
			break
		}
		events.path, changed = unitPath, true
	case iface == "org.freedesktop.DBus.Properties" && member == "PropertiesChanged" && events.path != "" && path == events.path:
		if len(message.Body) != 3 {
			fault()
			break
		}
		unitIface, ifaceOK := message.Body[0].(string)
		properties, propertiesOK := message.Body[1].(map[string]dbus.Variant)
		invalidated, invalidatedOK := message.Body[2].([]string)
		if !ifaceOK || !propertiesOK || !invalidatedOK {
			fault()
			break
		}
		if unitIface != systemdUnitInterface {
			return
		}
		if value, found := properties["InvocationID"]; found {
			invocation, ok := value.Value().([]byte)
			if !ok || (len(invocation) != 0 && len(invocation) != 16) {
				fault()
				break
			}
			encoded := ""
			if len(invocation) == 16 {
				encoded = hex.EncodeToString(invocation)
			}
			if encoded == "" || encoded == "00000000000000000000000000000000" {
				if events.activation.InvocationID != "" {
					fault()
					break
				}
			} else {
				if events.loaded && encoded == events.previousInvocation && events.activation.InvocationID == "" {
					return
				}
				if events.invocation != "" && events.invocation != encoded {
					fault()
					break
				}
				events.invocation, changed = encoded, true
			}
		}
		if events.activation.InvocationID != "" {
			for _, name := range invalidated {
				if name == "InvocationID" {
					fault()
				}
			}
		}
	}
	if changed {
		select {
		case events.wake <- struct{}{}:
		default:
		}
	}
}

func (events *managerStartupEventsV1) arm(unit string) error {
	events.mu.Lock()
	defer events.mu.Unlock()
	if unit == "" || events.unit != "" || events.err != nil {
		return sessionLeaseError("this original manager connection already owns a startup")
	}
	events.unit = unit
	return nil
}

// armLoadedV1 watches a loaded service's next actual job and invocation.
// It never waits for UnitNew, and retained old-invocation notifications cannot
// substitute for the new process created by the original StartUnit call.
func (events *managerStartupEventsV1) armLoadedV1(unit string, path dbus.ObjectPath, previous string) error {
	if unit == "" || !path.IsValid() || path == "/" {
		return sessionLeaseError("reactivation has no exact loaded native unit")
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if events.unit != "" || events.err != nil {
		return sessionLeaseError("this native connection already owns an activation")
	}
	events.unit, events.path, events.loaded, events.previousInvocation = unit, path, true, previous
	return nil
}

func (events *managerStartupEventsV1) waitJob(ctx context.Context, job dbus.ObjectPath) (dbus.ObjectPath, error) {
	for {
		events.mu.Lock()
		if events.err != nil {
			err := events.err
			events.mu.Unlock()
			return "", err
		}
		if events.job == "" {
			events.job = job
		}
		if events.job != job || events.observedJob != "" && events.observedJob != job || events.removedJob != "" && events.removedJob != job {
			events.mu.Unlock()
			return "", sessionLeaseError("the original manager returned another startup job")
		}
		if events.removedJob != "" && events.observedJob != "" {
			path, result := events.path, events.result
			correct := events.jobID == events.removedID && path != ""
			events.mu.Unlock()
			if !correct || result != "done" {
				return "", sessionLeaseError("the original direct-exec startup job did not complete")
			}
			return path, nil
		}
		events.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-events.wake:
		}
	}
}

func (events *managerStartupEventsV1) bindActivation(value unitActivationProperties) error {
	events.mu.Lock()
	defer events.mu.Unlock()
	if events.err != nil {
		return events.err
	}
	if events.loaded && value.InvocationID == events.previousInvocation {
		return sessionLeaseError("the native service did not create a new invocation")
	}
	if events.invocation != "" && events.invocation != value.InvocationID || events.activation.InvocationID != "" && !sameUnitActivationProperties(events.activation, value) {
		return sessionLeaseError("the first owned startup invocation changed")
	}
	events.invocation, events.activation = value.InvocationID, value
	return nil
}

func (events *managerStartupEventsV1) failure() error {
	events.mu.Lock()
	defer events.mu.Unlock()
	return errors.Join(events.err)
}

func (events *managerStartupEventsV1) markDispatched() {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.dispatched = true
}
func (events *managerStartupEventsV1) wasDispatched() bool {
	events.mu.Lock()
	defer events.mu.Unlock()
	return events.dispatched
}
func (events *managerStartupEventsV1) recordNegativeReply() {
	events.mu.Lock()
	defer events.mu.Unlock()
	if events.observedJob == "" && events.removedJob == "" {
		events.negativeReply = true
	}
	select {
	case events.wake <- struct{}{}:
	default:
	}
}
func (events *managerStartupEventsV1) waitOriginalEnd() error {
	for {
		events.mu.Lock()
		ended := !events.dispatched || events.negativeReply || events.unitRemoved && events.observedJob != "" && events.observedJob == events.removedJob && events.jobID == events.removedID
		events.mu.Unlock()
		if ended {
			return nil
		}
		// This is the actual request/controller stack, retaining its OP and
		// original native manager. An unknown or dead manager is not exit.
		<-events.wake
	}
}
