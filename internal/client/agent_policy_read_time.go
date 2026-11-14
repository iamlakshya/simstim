package client

import "github.com/iamlakshya/simstim/internal/api"

func projectedCreationTime(p *api.JSONProjection, span api.JSONSpan) error {
	parsed, err := readProjectedTime(p, span)
	if err != nil || parsed.instant.IsZero() {
		if err != nil {
			return err
		}
		return badSandboxState()
	}
	return nil
}
