package host

import (
	"context"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// NewContext starts only the explicit selected wall from the original request.
// The private bridge producer owns modern capture and native boot admission.
func NewContext(ctx context.Context, args NewArgs) (*SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	ctx, args, err := captureNewRequest(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := validateNewInputs(args); err != nil {
		return nil, err
	}
	if err := checkName(args.Name); err != nil {
		return nil, err
	}
	original := args
	args,err=PrepareRepositoryNewArgs(args)
	if err!=nil{return nil,err}
	if err := checkRequestedWall(args.Wall); err != nil {
		return nil, err
	}
	if err:=validateChosenWall(args);err!=nil{return nil,err}
	if args.Wall == "auto" {
		return nil, UserError("wall_unavailable", "the requested wall has not been resolved by its original client", "Choose the admitted wall through the current client before starting the sandbox.")
	}
	if args.Wall == "" && WallDriver != "" {
		return newWalled(ctx, args, WallDriver, original)
	}
	switch args.Wall {
	case "", "none":
		if len(args.VMNeedV1) != 0 || args.Runtime != nil || len(args.Layers) != 0 {
			return nil, UserError("not_walled", "an unwalled start cannot carry sandbox runtime or policy authority", "Choose an explicit admitted sandbox wall for these inputs.")
		}
		return newUnwalled(ctx, args)
	case "process":
		return newWalled(ctx, args, "process", original)
	case "gvisor":
		if args.Runtime == nil {
			return nil, UserError("wall_unavailable", "gvisor needs the original authenticated runtime manifest", "Prepare the runtime through the same original client request.")
		}
		return newWalled(ctx, args, "gvisor", original)
	case "microvm":
		need, err := vmneed.DecodeV1(args.VMNeedV1, runtime.GOARCH)
		if err != nil {
			return nil, vmNeedError("microvm needs a supported original client Need")
		}
		// The original captured request admits this decoded driver under its
		// reserved OP. H15's real parent then loans the SAME operation to the
		// owned early supervisor before any resource can construct.
		return newWalled(ctx,args,need.Driver,original)
	}
	return nil, UserError("bad_wall", "the selected wall is unknown", "Choose none, process, gvisor or microvm.")
}

func startSessionContext(ctx context.Context, manifest *Manifest, secrets map[string]api.Secret, fromFolder ...bool) (*SessionInfo, error) {
	reservation, err := reserveSessionID(manifest.ID)
	if err != nil {
		return nil, err
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		reservation.close()
		return nil, err
	}
	defer func() { reservation.close(); operation.Close() }()
	if manifest.ID == "" {
		manifest.ID = reservation.id
	}
	return startReservedSessionWithOperation(manifest, secrets, reservation, operation, fromFolder...)
}
