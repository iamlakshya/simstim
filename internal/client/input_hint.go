package client

import (
	"context"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

// HasAnyPendingSessionInputReceipt is a conservative local routing hint for
// name resolution. A marker can only select a no-wake connection. It proves
// no complete receipt, target identity, finish permission or payload source.
func HasAnyPendingSessionInputReceipt(ctx context.Context, original Host) (bool, error) {
	if err := bindRequestLocalSource(ctx, original); err != nil {
		return false, err
	}
	found := false
	err := withAgentPolicySnapshot(ctx, original, func(source io.ReaderAt, size int64, exists bool) error {
		if !exists {
			return nil
		}
		projection, err := api.NewJSONProjection(ctx, source, size)
		if err != nil {
			return err
		}
		root, err := projection.ObjectRoot()
		if err != nil {
			return err
		}
		var fields [3]api.JSONSpan
		if err := projection.Object(root, []string{"schema", "host", "sessions"}, func(field api.JSONField) error {
			if field.Known >= 0 {
				fields[field.Known] = field.Value
			}
			return nil
		}); err != nil {
			return err
		}
		schema := 0
		for value := 1; value <= 4; value++ {
			yes, err := projection.Literal(fields[0], string(rune('0'+value)))
			if err != nil {
				return err
			}
			if yes {
				schema = value
				break
			}
		}
		if schema == 0 {
			return badSandboxStateContext(ctx)
		}
		matches, err := projection.StringEqual(fields[1], original.Name)
		if err != nil || !matches {
			return badSandboxStateContext(ctx)
		}
		return projection.Object(fields[2], nil, func(row api.JSONField) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := projectedSessionID(projection, row.Name); err != nil {
				return err
			}
			return projection.Object(row.Value, []string{"input_operation"}, func(field api.JSONField) error {
				if field.Known < 0 {
					return nil
				}
				shape, err := projectSandboxReceiptSpans(ctx, projection, field.Value, schema, false)
				if err != nil {
					return err
				}
				if shape.Future {
					return futureSandboxReceiptError()
				}
				found = true
				return nil
			})
		})
	})
	return found, err
}
