package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

// RunAgentsGuardian owns its live memory scope before source discovery and
// before creating the canonical jail. Only helper/output FDs enter that jail;
// all later opened source capabilities are close-on-exec and remain here.
func RunAgentsGuardian(args []string) error {
	if len(args) != 1 {
		return agentsBuildFailure("the agents guardian needs its one owned scope")
	}
	unit := args[0]
	if err := validateConverterUnit(unit); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	helper, output := os.NewFile(3, "agents-helper"), os.NewFile(4, "fresh-agents-output")
	defer helper.Close()
	defer output.Close()
	return serveAgentsGuardian(ctx, unit, helper, output, os.Stdin, os.Stdout, imageAttachScope, runAgentsBuild)
}

func serveAgentsGuardian(ctx context.Context, unit string, helper, output *os.File, control io.Reader, result io.Writer, attach func(context.Context, string) (imageScopeInfo, error), build func(context.Context, []byte, *os.File, *os.File) (agentsBuildResultV1, error)) error {
	// Do not read the plan, walk names or parse file bodies before attachment.
	scope, err := attach(ctx, unit)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(result)
	if err := encoder.Encode(imageGuardianFrame{Phase: "ready", InvocationID: scope.InvocationID, ControlGroup: scope.ControlGroup}); err != nil {
		return err
	}
	raw, err := receiveImagePlan(control)
	if err != nil {
		return err
	}
	built, buildErr := build(ctx, raw, helper, output)
	data, err := json.Marshal(built)
	if err != nil {
		return err
	}
	return reportConverterDone(encoder, control, data, buildErr)
}

func runAgentsBuild(ctx context.Context, raw []byte, helper, output *os.File) (agentsBuildResultV1, error) {
	var plan agentsBuildPlanV1
	result := agentsBuildResultV1{Version: 1}
	fields, err := api.JSONObjectFields(raw, "version", "arch", "home", "search_path", "previous_source")
	if err != nil || len(fields) > 5 || json.Unmarshal(raw, &plan) != nil || plan.Version != 1 || plan.Arch != "amd64" && plan.Arch != "arm64" || plan.PreviousSource != "" && !supply.IsHex64(plan.PreviousSource) {
		return result, agentsBuildFailure("the closed agents build plan is invalid")
	}
	for _, name := range []string{"version", "arch", "home", "search_path"} {
		if _, found := fields[name]; !found {
			return result, agentsBuildFailure("the closed agents build plan is incomplete")
		}
	}
	for name := range fields {
		switch name {
		case "version", "arch", "home", "search_path", "previous_source":
		default:
			return result, agentsBuildFailure("the closed agents build plan has an unknown field")
		}
	}
	capture, err := captureInstalledAgents(ctx, plan.Home, plan.SearchPath)
	if err != nil {
		return result, err
	}
	defer capture.Close()
	before, err := image.AgentsSourceDigestV1(ctx, capture.inputs, capture.binaries)
	if err != nil {
		return result, err
	}
	result.SourceDigest = before
	result.Agents = append([]string{}, capture.kinds...)
	if before == plan.PreviousSource {
		result.Reused = true
		return result, nil
	}
	spec, err := converterSpec([]string{"/proc/self/fd/3", "agents-convert"}, []*os.File{helper, output})
	if err != nil {
		return result, err
	}
	during := ""
	data, err := runConverterJail(ctx, spec, func(writer io.Writer) error {
		var err error
		during, err = image.WriteAgentsTarV1(ctx, capture.inputs, capture.binaries, writer)
		return err
	})
	if err != nil {
		return result, err
	}
	if err := checkAgentsConvertResult(data); err != nil {
		return result, err
	}
	// Re-discover after all actual data reads; a replacement path/root must not
	// hide behind the inode still held by the first capture's descriptor.
	current, err := captureInstalledAgents(ctx, plan.Home, plan.SearchPath)
	if err != nil {
		return result, err
	}
	defer current.Close()
	after, err := image.AgentsSourceDigestV1(ctx, current.inputs, current.binaries)
	if err != nil {
		return result, err
	}
	if err := agentsSourceChanged(before, during, after); err != nil {
		return result, err
	}
	return result, nil
}

func validateConverterUnit(unit string) error {
	identifier := strings.TrimSuffix(strings.TrimPrefix(unit, brand.Name+"-image-"), ".scope")
	if unit != brand.Name+"-image-"+identifier+".scope" || !validImageInvocation(identifier) {
		return fmt.Errorf("invalid owned converter scope name")
	}
	return nil
}
