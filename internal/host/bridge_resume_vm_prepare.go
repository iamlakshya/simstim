package host

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The actual original cold phase2 binds its complete canonical request.
func (b *bridge) prepareResumeVMV1(service *bridgeResumeServiceV1, request proto.Request) (result *api.ResumePrepareResultV1, failure error) {
	var args api.ResumeVMPrepareArgsV1
	if request.ID == 0 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.V != 1 || !args.Prepared.Valid() {
		return nil, resumePreparationErrorV1("the original VM continuation is invalid")
	}
	fields, err := api.JSONObjectFields(request.Args, "v", "prepared", "start", "runtime_origin_v1")
	if err != nil || len(fields) < 3 || len(fields) > 4 {
		return nil, resumePreparationErrorV1("the original VM continuation fields are invalid")
	}
	startFields, err := api.JSONObjectFields(fields["start"])
	if err != nil {
		return nil, err
	}
	for _, field := range []string{"prepared", "named_ssh_keys", "secrets", "members", "secret_delivery", "protected_trusted_secret_values", "protected_trusted_secret_values_v1"} {
		if _, present := startFields[field]; present {
			return nil, resumePreparationErrorV1("VM preparation cannot carry private values")
		}
	}
	if raw, present := fields["runtime_origin_v1"]; present {
		value, err := api.DecodeRuntimeOriginV1(raw)
		if err != nil {
			return nil, err
		}
		args.RuntimeOrigin, err = api.EncodeRuntimeOriginV1(*value)
		if err != nil {
			return nil, err
		}
	}
	canonicalRequest, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	p := service.plan.Load()
	if p == nil || p.bridge != b || p.token != args.Prepared.Token || p.consumed {
		return nil, resumePreparationErrorV1("this connection does not own that cold VM preparation")
	}
	metadata, err := api.ResumePreparationMetadataV1(args.Start)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	if !p.pendingVM {
		if !bytes.Equal(encoded, p.metadata) || p.vm == nil || !bytes.Equal(canonicalRequest, p.vmRequest) {
			return nil, resumePreparationErrorV1("the cold VM continuation changed its original request")
		}
		if err := p.check(p.lifetime); err != nil {
			return nil, err
		}
		result := p.result()
		return &result, nil
	}
	// Reject all private carriers rather than silently stripping an attempt.
	clean, err := json.Marshal(args.Start)
	if err != nil || !bytes.Equal(clean, encoded) {
		return nil, resumePreparationErrorV1("VM preparation cannot carry private values")
	}
	original := metadata
	original.VMNeedV1 = nil
	originalEncoded, err := json.Marshal(original)
	if err != nil || !bytes.Equal(originalEncoded, p.metadata) {
		return nil, resumePreparationErrorV1("the fresh VM selection changed its original metadata")
	}
	selection, err := p.bindVMSelectionV1(p.lifetime, request.Args, metadata, args.RuntimeOrigin)
	if err != nil {
		return nil, err
	}
	p.vm, p.args = selection, metadata
	defer func() {
		if failure == nil {
			return
		}
		cleanup := p.close()
		failure = errors.Join(failure, cleanup)
		if cleanup == nil {
			service.settled = p.token
			service.plan.Store(nil)
		}
	}()
	// Retain the original owner on every partial failure. Cleanup proves native
	// closure and settles its SAME charge before another preparation can start.
	if err := p.finishColdPreparationV1(); err != nil {
		return nil, err
	}
	p.pendingVM = false
	p.metadata = bytes.Clone(encoded)
	p.vmRequest = bytes.Clone(canonicalRequest)
	if err := p.check(p.lifetime); err != nil {
		return nil, err
	}
	view := p.result()
	return &view, nil
}
