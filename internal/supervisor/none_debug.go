package supervisor

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) noneDebugRequestV1(client *client, request proto.Request, frame proto.Frame) (bool, bool) {
	if !s.unwalledV1() {
		return false, false
	}
	switch request.Op {
	case api.OpDebugStart, api.OpDebugStop, api.OpDebugProfile, api.OpDebugSave:
		return true, s.pass(client, frame)
	}
	return false, false
}

// This projects diagnostic DATA only from the fixed existing holder record.
// It never creates native facts, a source, a path grant or execution rights.
func (s *Supervisor) noneDebugUntilV1() *time.Time {
	if !s.unwalledV1() {
		return nil
	}
	run, err := cachefs.OpenPrivateRoot(paths.SandboxRoot(s.owner().id), false)
	if err != nil {
		return nil
	}
	defer run.Close()
	members, err := cachefs.OpenPrivateChild(run, "members", false)
	if err != nil {
		return nil
	}
	defer members.Close()
	member, err := cachefs.OpenPrivateChild(members, s.id, false)
	if err != nil {
		return nil
	}
	defer member.Close()
	file, err := cachefs.OpenFile(member, "holder.json", os.O_RDONLY, 0600)
	if err != nil {
		return nil
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxEvent+1))
	if err != nil || len(raw) > maxEvent {
		return nil
	}
	var value struct {
		Until *time.Time `json:"debug_until"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Until == nil || value.Until.IsZero() {
		return nil
	}
	return value.Until
}
