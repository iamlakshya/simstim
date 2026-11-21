package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// RunPurgePayload never enters normal placement admission or installer setup.
// It verifies its mapped staged bytes and accepts two fixed terminal roles.
func RunPurgePayload(ctx context.Context, input io.Reader, output io.Writer) (returned error) {
	raw, err := io.ReadAll(io.LimitReader(input, 32<<10+1))
	if err != nil || len(raw) > 32<<10 {
		return placementError("the cleanup payload exceeded its fixed plan bound")
	}
	var plan api.PurgeCleanupV1
	if !api.ExactJSONFields(raw, plan) || json.Unmarshal(raw, &plan) != nil || plan.Version != 1 || !ValidHostID(plan.HostID) || !ValidHostID(plan.Nonce) || plan.Install.Version != 1 || !ValidHostID(plan.Install.Nonce) || plan.Install.Arch != runtime.GOARCH || len(plan.Install.Items) != 1 || !validInstallItemV1(plan.Install.Items[0]) || plan.Install.Items[0].Kind != supply.KindHelper || plan.Action != "report" && plan.Action != "commit" || plan.Action == "report" && plan.Witness != nil || plan.Action == "commit" && (plan.Witness == nil || !plan.Witness.Prepared() || plan.Witness.HostID != plan.HostID || plan.Witness.Nonce != plan.Nonce) {
		return placementError("the cleanup payload has an unknown fixed role or original attempt")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	stage, err := checkStagedPurgePayloadV1(ctx, plan.Install)
	if err != nil {
		return err
	}
	// Its original stage is removed while this process still retains its code.
	// No stage path or mapped binary becomes an ordinary installation.
	if err := stage.close(); err != nil {
		return err
	}
	if plan.Action == "commit" {
		if err := CommitPurgeGuardContext(ctx, *plan.Witness); err != nil {
			return err
		}
		_, err = io.WriteString(output, "__PURGE_COMMITTED__\n")
		return err
	}
	report, err := readPreparedPurgeReportV1(ctx, plan.HostID, plan.Nonce)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(report)
}

func checkStagedPurgePayloadV1(ctx context.Context, plan api.InstallPlanV1) (*installStageV1, error) {
	name, mapped, err := mappedExecutable()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if filepath.Dir(name) != filepath.Join(home, "."+brand.Name+"-cleanup-"+plan.Nonce) || filepath.Base(name) != brand.HostBinary {
		return nil, placementError("the cleanup command is not its original staged executable")
	}
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(name), false)
	if err != nil {
		return nil, err
	}
	file, err := cachefs.OpenFile(root, brand.HostBinary, os.O_RDONLY, 0o755)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	fail := func(err error) (*installStageV1, error) { _ = file.Close(); _ = root.Close(); return nil, err }
	info, err := cachefs.ValidateFile(root, brand.HostBinary, file, 0o755)
	if err != nil || !os.SameFile(mapped, info) {
		return fail(placementError("the staged cleanup file differs from its running mapped code"))
	}
	item := plan.Items[0]
	if err := supply.CheckOpenContext(ctx, supply.Item{Kind: item.Kind, Name: item.Name, Arch: plan.Arch, SHA256: item.SHA256, Size: item.Size}, file); err != nil {
		return fail(err)
	}
	directory, err := root.Open(".")
	if err != nil {
		return fail(err)
	}
	parent, err := directory.Stat()
	_ = directory.Close()
	if err != nil {
		return fail(err)
	}
	return &installStageV1{root: root, file: file, directory: refOf(parent), executable: refOf(info), nonce: plan.Nonce}, nil
}

func readPreparedPurgeReportV1(ctx context.Context, hostID, nonce string) (api.PurgeResult, error) {
	var result api.PurgeResult
	scope, path, err := placementScope()
	if err != nil {
		return result, err
	}
	err = withPlacementHome(ctx, func(_ *os.Root) error {
		guard, err := cachefs.OpenPrivateRoot(path, false)
		if err != nil {
			return err
		}
		defer guard.Close()
		control, found, err := readPlacementControl(guard, scope)
		if err != nil {
			return err
		}
		if !found || control.HostID != hostID || control.Nonce != nonce || control.Phase != "finalising" || control.GuardWitness == nil || control.Report == nil || !control.Report.HostComplete || !terminalStagesComplete(control.Report.Done) {
			return placementError("the original host purge has no complete retained finalisation")
		}
		original := *control.GuardWitness
		marker, err := cachefs.OpenFile(guard, ".final-marker-"+original.Token, os.O_RDONLY, 0o600)
		if err != nil {
			return err
		}
		defer marker.Close()
		info, err := cachefs.ValidateFile(guard, ".final-marker-"+original.Token, marker, 0o600)
		if err != nil || !nativePurgeRef(original.Marker).samePreparedIdentity(info) {
			return placementError("the retained final marker changed its original prepared inode")
		}
		raw, err := io.ReadAll(io.LimitReader(marker, 16<<10+1))
		if err != nil || len(raw) > 16<<10 {
			return placementError("the retained final marker is incomplete")
		}
		prepared := original
		prepared.Marker = apiPurgeRef(refOf(info))
		prepared.MarkerSHA256 = footprintDigest(raw)
		if !prepared.Prepared() {
			return placementError("the retained final marker has no complete native content witness")
		}
		if _, err := readPurgeMarkerV1(ctx, guard, prepared); err != nil {
			return err
		}
		result = *control.Report
		result.GuardWitness = &prepared
		return nil
	})
	return result, err
}
