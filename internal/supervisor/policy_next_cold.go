package supervisor

import (
	"errors"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// commitOriginalPolicyNextV1 uses the actual supervisor's same save turn and
// publishes its live metadata only after durable success. An outside clear
// followed by an old in-memory tick could otherwise resurrect the capsule.
func (s *Supervisor) commitOriginalPolicyNextV1(line *host.StartupLineV1, id uint64) error {
	if s.bootOwner == nil || line == nil || id == 0 {
		return execRefusal("the pending policy commit has no original ready native owner")
	}
	s.saveMu.Lock()
	var snapshot host.Manifest
	err := s.writeManifestChange(&snapshot, false, nil, nil, func(prospective *host.Manifest) error {
		if err := s.bootOwner.CheckPolicyNextAppliedV1(line, prospective); err != nil {
			return err
		}
		prospective.PolicyNext = nil
		return nil
	}, func(committed *host.Manifest) { s.m.PolicyNext = nil })
	s.saveMu.Unlock()
	if errors.Is(err, host.ErrPolicyNextUnappliedV1) {
		err = nil
	}
	if err != nil {
		return line.WriteFrame(proto.TResponse, proto.Response{ID: id, Error: toProtoErr(err)})
	}
	return line.WriteFrame(proto.TResponse, proto.Response{ID: id, OK: true})
}
