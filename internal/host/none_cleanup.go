package host

import (
	"github.com/iamlakshya/simstim/internal/cachefs"
	"time"
)

// NoneCleanupV1 records only the original host-account creator's successful
// cleanup. Numbers are refusal selectors, never process handles or permissions.
// It is not accepted over the wire and is never a walled stopped subject.
type NoneCleanupV1 struct {
	Version         int               `json:"v"`
	Scope           string            `json:"scope"`
	Boot            string            `json:"boot"`
	Generation      string            `json:"generation"`
	SupervisorPID   int               `json:"supervisor_pid"`
	InitPID         int               `json:"init_pid"`
	SupervisorBirth string            `json:"supervisor_birth"`
	InitBirth       string            `json:"init_birth"`
	At              time.Time         `json:"at"`
	Directory       cachefs.Reference `json:"directory"`
	Holder          cachefs.Reference `json:"holder"`
}

func validNoneCleanupV1(m *Manifest, boot string) bool {
	if !IsNoneSupervisor(m) || m.SandboxOf != "" || m.NoneCleanup == nil || m.Life == nil || m.Life.Phase != LifeStopped || m.Life.End == nil || m.Life.End.Kind != EndStop {
		return false
	}
	c := m.NoneCleanup
	return c.Version == 1 && c.Scope == "n5" && c.Boot == boot && c.Boot == m.Run.BootID && c.Generation != "" && c.Generation == m.Run.Generation && c.SupervisorPID > 0 && c.SupervisorPID == m.Run.HolderPID && c.InitPID > 0 && c.InitPID != c.SupervisorPID && validNoneBirthV1(c.SupervisorBirth) && validNoneBirthV1(c.InitBirth) && !c.At.IsZero() && !c.At.Before(m.Life.End.At) && c.Directory.Known() && c.Directory.Directory && c.Holder.Known() && !c.Holder.Directory && c.Holder.Mode == 0600
}
