package proto

// CtlMemberResumePrepare retires one actual ended holder and reserves its new
// run through the same existing member preparation and Store machinery.
const CtlMemberResumePrepare = "member.resume.prepare-v1"

// CtlMemberResumePrepareArgs binds retirement to the previous owned run.
// It cannot request another guest, policy, image or named-key set.
type CtlMemberResumePrepareArgs struct {
	Version     int          `json:"version"`
	PreviousRun string       `json:"previous_run"`
	Start       CtlStartArgs `json:"start"`
}
