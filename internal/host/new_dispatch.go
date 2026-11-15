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
	if err := checkRequestedWall(args.Wall); err != nil {
		return nil, err
	}
	if args.Wall == "auto" {
		return nil, UserError("wall_unavailable", "the requested wall has not been resolved by its original client", "Choose the admitted wall through the current client before starting the sandbox.")
	}
	if args.Wall == "" && WallDriver != "" {
		return newWalled(ctx, args, WallDriver)
	}
	switch args.Wall {
	case "", "none":
		if len(args.VMNeedV1) != 0 || args.Runtime != nil || len(args.Layers) != 0 {
			return nil, UserError("not_walled", "an unwalled start cannot carry sandbox runtime or policy authority", "Choose an explicit admitted sandbox wall for these inputs.")
		}
		return newUnwalled(ctx, args)
	case "process":
		return newWalled(ctx, args, "process")
	case "gvisor":
		if args.Runtime == nil {
			return nil, UserError("wall_unavailable", "gvisor needs the original authenticated runtime manifest", "Prepare the runtime through the same original client request.")
		}
		return newWalled(ctx, args, "gvisor")
	case "microvm":
		need, err := vmneed.DecodeV1(args.VMNeedV1, runtime.GOARCH)
		if err != nil {
			return nil, vmNeedError("microvm needs a supported original client Need")
		}
		// The next producer cut must supply the native BootOwnerV1 on the
		// same early startup line before this selected driver can construct.
		// Keep this real missing producer explicit; never select inventory or
		// fall through to ordinary/process startup.
		return nil, UserError("wall_unavailable", "the selected "+need.Driver+" startup has no admitted native boot owner", "Use a helper with the complete native startup loan and original Need producer.")
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
