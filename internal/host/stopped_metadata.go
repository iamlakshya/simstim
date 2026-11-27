package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

type stopDiagnosticsV1 struct {
	Version    int    `json:"version"`
	Generation string `json:"generation"`
	Life       *Life  `json:"life"`
	Status     Status `json:"status"`
}

// explicitStopProjectionV1 builds diagnostic/status DATA only. Its result
// supplies no native stop, source, clean-exit, capture or start authority.
// A concrete native stop publisher must separately prove its own closure.
// Ordinary, N5 and manager custody publishers keep distinct native checkers.
func explicitStopProjectionV1(saved *Manifest, at time.Time) (Manifest, error) {
	if saved == nil {
		return Manifest{}, memberError("the explicit stop has no original manifest")
	}
	next := CloneManifest(*saved)
	next.Armed, next.AutomaticRestore = false, nil
	if saved.Life != nil && saved.Life.Phase == LifeStopped && saved.Life.End != nil && saved.Life.End.Kind == EndStop && saved.Status.State == StateStopped {
		return next, nil
	}
	if len(saved.StopDiagnosticsV1) != 0 {
		var prior stopDiagnosticsV1
		if !api.ExactJSONFields(saved.StopDiagnosticsV1, prior) || json.Unmarshal(saved.StopDiagnosticsV1, &prior) != nil || prior.Version != 1 {
			return Manifest{}, UserError("stop_diagnostics", "the earlier stop diagnostics have an unsupported format", "Keep the preserved manifest and inspect it with the matching helper before retrying.")
		}
	}
	diagnostic, err := json.Marshal(stopDiagnosticsV1{Version: 1, Generation: saved.Run.Generation, Life: next.Life, Status: next.Status})
	if err != nil {
		return Manifest{}, err
	}
	next.StopDiagnosticsV1 = diagnostic
	before := Life{}
	if saved.Life != nil {
		before = *saved.Life
	}
	end := LifeEnd{Kind: EndStop, Reason: "stopped by request", At: at}
	if before.End != nil {
		end.OOM, end.OOMAgent, end.Claimed = before.End.OOM, before.End.OOMAgent, before.End.Claimed
		if before.End.Reason != "" {
			end.Reason += ": " + before.End.Reason
		}
	}
	before.Phase, before.End = LifeStopped, &end
	next.Life = &before
	next.Status = Status{State: StateStopped, Source: "host", Since: at, Reason: end.Reason}
	next.UpdatedAt = at
	return next, nil
}

// openExactStoppedMetadataV1 keeps the original inode open through the CAS.
// This is metadata comparison only; it cannot establish a native lifetime.
func openExactStoppedMetadataV1(root *os.Root, saved *Manifest) (_ *os.File, _ os.FileInfo, returned error) {
	file, err := cachefs.OpenFile(root, "manifest.json", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, nil, errors.Join(memberError("the original stopped manifest is missing"), err)
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, file.Close())
		}
	}()
	before, err := cachefs.ValidateFile(root, "manifest.json", file, 0o600)
	if err != nil {
		return nil, nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(proto.MaxFrame)+1))
	var current Manifest
	if err != nil || len(raw) > proto.MaxFrame || json.Unmarshal(raw, &current) != nil {
		return nil, nil, errors.Join(memberError("the original stopped source changed before publication"), err)
	}
	want, wantErr := saved.MarshalJSON()
	got, gotErr := current.MarshalJSON()
	if wantErr != nil || gotErr != nil || !bytes.Equal(want, got) {
		return nil, nil, errors.Join(memberError("the original stopped source changed before publication"), wantErr, gotErr)
	}
	complete = true
	return file, before, nil
}
