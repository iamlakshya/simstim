package classify

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
)

// NewConfigured selects a judge from trusted resolved settings. The caller
// must reject repository-controlled executable, endpoint and key settings
// before constructing its model capability or calling this factory.
func NewConfigured(values api.ClassifierValues, protocol ModelProtocol, caller ModelCaller) (Judge, error) {
	kind, maxScope, err := configuredKindScope(values)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "none":
		return noJudge{scope: maxScope}, nil
	case "command":
		if values.Command == nil {
			return nil, errCommand
		}
		judge, err := NewCommand(*values.Command, maxScope)
		return requireReasonSanitizer(judge, err)
	case "model":
		if values.Model == nil {
			return nil, errModel
		}
		judge, err := NewModel(*values.Model, protocol, maxScope, caller)
		return requireReasonSanitizer(judge, err)
	default:
		return nil, errors.New("unknown judge kind. Try: configure none, command or model")
	}
}

type noJudge struct{ scope string }

func (judge noJudge) Decide(_ context.Context, _ Input) (Result, error) {
	return Result{Action: "unsure", Reason: "no judge configured", Scope: judge.scope}, nil
}

func configuredKindScope(values api.ClassifierValues) (string, string, error) {
	kind, maxScope := "none", "session"
	if values.Judge != nil {
		kind = *values.Judge
	}
	if values.MaxScope != nil {
		maxScope = *values.MaxScope
	}
	if maxScope != "once" && maxScope != "session" {
		return "", "", errAnswer
	}
	return kind, maxScope, nil
}

// CheckConfigured validates the same pure adapter metadata without receiving
// a model caller, key, endpoint permission or process execution capability.
func CheckConfigured(values api.ClassifierValues, protocol ModelProtocol) error {
	kind, scope, err := configuredKindScope(values)
	if err != nil {
		return err
	}
	switch kind {
	case "none":
		return nil
	case "command":
		if values.Command == nil {
			return errCommand
		}
		_, err := NewCommand(*values.Command, scope)
		return err
	case "model":
		if values.Model == nil || !validModelMetadata(*values.Model, protocol, scope) {
			return errModel
		}
		return nil
	default:
		return errors.New("unknown judge kind. Try: configure none, command or model")
	}
}
