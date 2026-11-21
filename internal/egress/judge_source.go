package egress

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

type judgeSourceSnapshotKey struct{}
type judgeSourceSnapshot struct {
	server *Server
	state  *memberState
}

func (judgeSourceSnapshot) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}

// withJudgeSourceSnapshotLocked binds one call to the actual immutable
// registry after the invocation owner has source-checked its complete DTO.
// The owner holds policyMu. Neither the pointer nor a value lookup is public.
func (s *Server) withJudgeSourceSnapshotLocked(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	ctx = context.WithValue(ctx, judgeSourceSnapshotKey{}, judgeSourceSnapshot{server: s, state: s.currentMembers()})
	return classify.WithReasonSanitizer(ctx, s.sanitizeJudgeReason)
}

func (s *Server) judgeSourceSnapshotLocked(ctx context.Context) (*memberState, bool) {
	if ctx == nil {
		return nil, false
	}
	snapshot, ok := ctx.Value(judgeSourceSnapshotKey{}).(judgeSourceSnapshot)
	return snapshot.state, ok && snapshot.server == s && snapshot.state != nil && snapshot.state == s.currentMembers()
}

// Every known source name is a dependency of foreign model input. Binding
// only the model key would miss another credential changing to an input's
// exact host or task. Mutators must also retire calls before adding a new
// source name or variant; an old dependency list cannot name a future value.
func judgeSourceNames(state *memberState) []string {
	if state == nil {
		return nil
	}
	names := slices.Sorted(maps.Keys(state.secrets))
	for name := range state.retired {
		names = append(names, name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}
