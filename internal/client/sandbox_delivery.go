package client

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

func decodeSandboxDeliveryContext(ctx context.Context, raw json.RawMessage, schema int, row *SandboxPolicy) error {
	p, err := api.NewJSONBytesProjection(ctx, raw)
	if err != nil {
		return err
	}
	root, err := p.ObjectRoot()
	if err != nil {
		return err
	}
	var fields [4]api.JSONSpan
	err = p.Object(root, []string{"secrets", "lineage", "delivery_intent", "wall_intent"}, func(field api.JSONField) error {
		if field.Known >= 0 {
			fields[field.Known] = field.Value
		}
		return nil
	})
	if err != nil || fields[2].End == 0 {
		return err
	}
	allowed, err := deliveryAllowedSpan(p, fields[0], fields[1], row.lineage != nil)
	if err != nil {
		return err
	}
	exposed, err := deliveryExposedSpan(p, fields[3])
	if err != nil {
		return err
	}
	row.DeliveryIntent, row.FutureDelivery, err = projectSandboxDelivery(ctx, p, fields[2], allowed, exposed, schema,
		sandboxDeliveryContext{hostIDNonempty: row.HostID != "", wallNone: row.CreationWall == "none", sshAgent: row.SSHAgent, futureLineage: row.FutureLineage}, true)
	if err != nil {
		return badSandboxStateContext(ctx)
	}
	return ctx.Err()
}

func deliveryAllowedSpan(p *api.JSONProjection, secrets, lineage api.JSONSpan, knownLineage bool) (api.JSONSpan, error) {
	if !knownLineage {
		return secrets, nil
	}
	var allowed api.JSONSpan
	err := p.Object(lineage, []string{"allowed_names"}, func(field api.JSONField) error {
		if field.Known == 0 {
			allowed = field.Value
		}
		return nil
	})
	return allowed, err
}

func retainProjectedString(p *api.JSONProjection, span api.JSONSpan) (string, error) {
	var kept strings.Builder
	err := p.WalkString(span, func(r rune) error { _, err := kept.WriteRune(r); return err })
	return kept.String(), err
}

func futureDeliveryError() error {
	return &UserError{Msg: "this newer secret delivery record cannot authorise values or agent access in this client",
		Hint: "Upgrade the client before sending values or adding a member.", Code: "secret_delivery", Kind: KindRefused}
}

// commitSandboxBoundSchema changes only the coarse read barrier. Main must
// first seal local intent and capabilities, then hold its actual HostLock.
// It does not record an actual-sent row or grant permission to run commands.
func commitSandboxBoundSchema(ctx context.Context, h Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidHostName(h.Name) {
		return badHostNameError(h.Name)
	}
	held, ok := ctx.Value(heldKey{h.Name}).(*heldLock)
	if !ok || held.gone.Load() {
		return &UserError{Msg: "the bound delivery barrier needs the original host's publication lock",
			Hint: "Prepare bound values through the original session producer.", Code: "grant_scope", Kind: KindRefused}
	}
	return withSandboxFile(ctx, h, true, func(file *sandboxFile) error {
		if file.schema == 4 {
			return errStateNoChange
		}
		file.schema, file.root["schema"] = 4, json.RawMessage("4")
		return ctx.Err()
	})
}

func deliveryExposedSpan(p *api.JSONProjection, wall api.JSONSpan) (api.JSONSpan, error) {
	if wall.End == 0 {
		return api.JSONSpan{}, nil
	}
	var exposed api.JSONSpan
	err := p.Object(wall, []string{"exposed"}, func(field api.JSONField) error {
		if field.Known == 0 {
			exposed = field.Value
		}
		return nil
	})
	return exposed, err
}

func sameSandboxDelivery(a, b *SandboxPolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.FutureDelivery == b.FutureDelivery && reflect.DeepEqual(a.DeliveryIntent, b.DeliveryIntent)
}
