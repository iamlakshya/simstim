package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// RestoreContext owns one host-wide restore flow and its two workers. The
// original operation, native source and fresh admission still authorise each
// individual start. Cancellation joins those workers before releasing the gate.
func RestoreContext(ctx context.Context, all bool) (_ []RestoreResult, returned error) {
	if ctx == nil {
		return nil, UserError("bad_request", "restore has no request lifetime", "Retry host restore through the current client.")
	}
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(paths.SessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, stateMetadataFailure(err)
	}
	var candidates []restoreCandidateV1
	for _, entry := range entries {
		if entry.IsDir() && validID(entry.Name()) {
			candidates = append(candidates, restoreCandidateV1{id: entry.Name(), activity: restoreActivityV1(entry.Name())})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	gate, err := beginRestoreGateV1(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, stateMetadataFailure(gate.Close())) }()
	boot := BootID()
	rows := runRestoreCandidatesV1(candidates, func(id string) (RestoreResult, bool) {
		if err := gate.Check(); err != nil {
			row := lazyRestoreFailureV1(stateMetadataFailure(err))
			row.ID = id
			return row, true
		}
		return restoreOneForGateV1(gate.publisher.ctx, id, boot, all, gate)
	})
	if err := gate.Check(); err != nil {
		return rows, stateMetadataFailure(err)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func (b *bridge) restoreRequestV1(request proto.Request) {
	var args struct {
		All bool `json:"all"`
	}
	fields, err := api.JSONObjectFields(request.Args, "all")
	all, present := fields["all"]
	all = bytes.TrimSpace(all)
	if err != nil || len(fields) > 1 || present && !bytes.Equal(all, []byte("true")) && !bytes.Equal(all, []byte("false")) || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil {
		b.reply(request.ID, nil, UserError("bad_request", "restore needs the supported all setting", "Use the current client to request host restore."))
		return
	}
	rows, err := RestoreContext(b.ctx, args.All)
	b.reply(request.ID, rows, err)
}
