package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/secretbind"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type agentSettingsVisitor struct {
	schema            *agentSchema
	ctx               context.Context
	entered, complete bool
	header            secretbind.HeaderValidator
	format            secretbind.FormatValidator
	authority         secretbind.AuthorityValidator
	stream            [256]byte
}

func (v *agentSettingsVisitor) clear() {
	if v.schema != nil {
		v.schema.clear()
	}
	v.ctx = nil
	v.entered, v.complete = false, false
	v.header = secretbind.HeaderValidator{}
	v.format = secretbind.FormatValidator{}
	v.authority = secretbind.AuthorityValidator{}
	clear(v.stream[:])
}

func (v *agentSettingsVisitor) EnterTable(path tomlinspect.PathCursor, table tomlinspect.TableCursor) (err error) {
	// This starts after the serial mode index lease is held. No shared
	// visitor workspace is reset while another inspection can use it.
	v.clear()
	defer func() {
		if err != nil {
			v.clear()
		}
	}()
	depth, err := path.Depth()
	if err != nil {
		return err
	}
	if depth != 0 || v.schema == nil {
		return errAgentSettingsSchema
	}
	v.ctx, err = path.Context()
	if err != nil {
		return err
	}
	v.entered = true
	root := table.Value()
	var shape error
	if err := v.schema.root.preflight(root, &shape); err != nil {
		return err
	}
	if shape != nil {
		return shape
	}
	if err := v.schema.root.typed(root); err != nil {
		return err
	}
	if value, present := v.schema.root.slot("secret_tiers"); present {
		if err := v.secretTiers(value); err != nil {
			return err
		}
	}
	if err := v.secretDefinitions(); err != nil {
		return err
	}
	v.complete = true
	return nil
}
func (v *agentSettingsVisitor) LeaveTable(path tomlinspect.PathCursor) error {
	if !v.entered || !v.complete {
		v.clear()
		return errAgentSettingsSchema
	}
	depth, err := path.Depth()
	if err != nil {
		v.clear()
		return err
	}
	if depth != 0 {
		v.clear()
		return errAgentSettingsSchema
	}
	return nil
}
func (v *agentSettingsVisitor) EnterArray(tomlinspect.PathCursor, tomlinspect.ArrayCursor) error {
	v.clear()
	return errAgentSettingsSchema
}
func (v *agentSettingsVisitor) LeaveArray(tomlinspect.PathCursor) error {
	v.clear()
	return errAgentSettingsSchema
}
func (v *agentSettingsVisitor) Scalar(tomlinspect.PathCursor, tomlinspect.ScalarCursor) error {
	v.clear()
	return errAgentSettingsSchema
}
func (v *agentSettingsVisitor) Finish() error {
	defer v.clear()
	if !v.entered || !v.complete || v.ctx == nil {
		return errAgentSettingsSchema
	}
	return v.ctx.Err()
}
