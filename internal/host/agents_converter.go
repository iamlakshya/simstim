package host

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
)

type agentsBuildPlanV1 struct {
	Version        int    `json:"version"`
	Arch           string `json:"arch"`
	Home           string `json:"home"`
	SearchPath     string `json:"search_path"`
	PreviousSource string `json:"previous_source,omitempty"`
}
type agentsBuildResultV1 struct {
	Version      int      `json:"version"`
	SourceDigest string   `json:"source_digest"`
	Agents       []string `json:"agents"`
	Reused       bool     `json:"reused"`
}

func buildAgentsScoped(ctx context.Context, plan agentsBuildPlanV1, output *os.File) (agentsBuildResultV1, error) {
	var result agentsBuildResultV1
	raw, err := json.Marshal(plan)
	if err != nil {
		return result, err
	}
	var done imageGuardianFrame
	if err := convertScoped(ctx, convertInstalledAgents, raw, nil, output, &done); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, agentsBuildFailure(err.Error())
	}
	if err := decodeAgentsBuildResult(done.Output, &result); err != nil {
		return result, err
	}
	if !result.Reused {
		if _, err := image.InspectAgentsV1(output); err != nil {
			return result, err
		}
	}
	return result, nil
}

func decodeAgentsBuildResult(raw []byte, result *agentsBuildResultV1) error {
	fields, err := api.JSONObjectFields(raw, "version", "source_digest", "agents", "reused")
	if err != nil || len(fields) != 4 {
		return agentsBuildFailure("the guardian returned malformed producer metadata")
	}
	if json.Unmarshal(raw, result) != nil || result.Version != 1 || !image.ValidDigest("sha256:"+result.SourceDigest) {
		return agentsBuildFailure("the guardian returned invalid producer identity")
	}
	return nil
}

// RunAgentsConvert is the fixed descriptor-only jailed converter operation.
// Its ordinary stdin is an owned raw tar pipe, never a path or fd0 file grant.
func RunAgentsConvert(args []string, input io.Reader, result io.Writer) error {
	if len(args) != 0 {
		return agentsBuildFailure("the agents converter accepts no path or descriptor arguments")
	}
	if err := imageWorkerConfinement(); err != nil {
		return err
	}
	output := os.NewFile(4, "fresh-agents-output")
	defer output.Close()
	err := image.ConvertAgentsV1(context.Background(), input, output)
	response := struct {
		Version int    `json:"version"`
		Error   string `json:"error,omitempty"`
	}{Version: 1}
	if err != nil {
		response.Error = err.Error()
	}
	return json.NewEncoder(result).Encode(response)
}

func checkAgentsConvertResult(raw []byte) error {
	var result struct {
		Version int    `json:"version"`
		Error   string `json:"error,omitempty"`
	}
	fields, err := api.JSONObjectFields(raw, "version", "error")
	if err != nil || len(fields) > 2 || json.Unmarshal(raw, &result) != nil || result.Version != 1 {
		return agentsBuildFailure("the jailed agents converter returned malformed output")
	}
	if result.Error != "" {
		return agentsBuildFailure(result.Error)
	}
	return nil
}

func agentsSourceChanged(before, during, after string) error {
	if before == "" || before != during || before != after {
		return agentsBuildFailure("installed agents changed during their disk build")
	}
	return nil
}
