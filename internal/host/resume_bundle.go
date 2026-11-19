package host

import (
	"fmt"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func checkedResumeMembers(primary string, children []*Manifest, got []api.MemberResumeArgs) (map[string]map[string]api.Secret, error) {
	if len(children) != len(got) {
		return nil, snapshotChanged()
	}
	expected := map[string]*Manifest{}
	for _, child := range children {
		expected[child.ID] = child
	}
	values := make(map[string]map[string]api.Secret, len(got))
	for _, member := range got {
		child, ok := expected[member.ID]
		if !ok || member.ID == primary {
			return nil, snapshotChanged()
		}
		if _, dup := values[member.ID]; dup {
			return nil, snapshotChanged()
		}
		checked, err := checkedResumeValues(child.Secrets, member.Secrets)
		if err != nil {
			return nil, err
		}
		values[member.ID] = checked
	}
	return values, nil
}
func checkedResumeValues(want []string, got map[string]api.Secret) (map[string]api.Secret, error) {
	if _, err := checkSecrets(got); err != nil {
		return nil, err
	}
	checked := make(map[string]api.Secret, len(got))
	for name, value := range got {
		if !slices.Contains(want, name) {
			return nil, UserError("bad_secret", "resume supplied a name outside the recorded session", "Fetch only this session's original requested names.")
		}
		checked[name] = value
	}
	return checked, nil
}

// resumeBundleLocked is called with one canonical PRIMARY op.lock. It never
// reacquires it, and completes all ID/argv/value checks before stopping anyone.
func resumeBundleLocked(original string,a api.ResumeArgs,root *Manifest,operation *SessionOperation)(*ResumeResult,error){
	if err:=validateRestartWall(root);err!=nil{return nil,err}
	if operation==nil || !operation.Valid(root.ID){return nil,snapshotChanged()}
	if err := CheckSandboxSnapshotLocked(original, root.ID, a.Snapshot); err != nil {
		return nil, err
	}
	children, err := SandboxMembers(root.ID)
	if err != nil {
		return nil, err
	}
	childValues, err := checkedResumeMembers(root.ID, children, a.Members)
	if err != nil {
		return nil, err
	}
	delivery, err := prepareResumeSecretDelivery(root, a.SecretDelivery, a.Secrets)
	if err != nil {
		return nil, err
	}
	suppliedIntent := make(map[string]*api.SecretDeliveryIntent, len(a.Members))
	for _, member := range a.Members {
		suppliedIntent[member.ID] = member.SecretDelivery
	}
	childDeliveries := make(map[string]*api.SecretDeliveryIntent, len(children))
	for _, child := range children {
		checked, err := prepareResumeSecretDelivery(child, suppliedIntent[child.ID], childValues[child.ID])
		if err != nil {
			return nil, err
		}
		childDeliveries[child.ID] = checked
	}
	primaryValues, err := checkedResumeValues(root.Secrets, a.Secrets)
	if err != nil {
		return nil, err
	}
	// A living sandbox/member is never restarted to inject a new environment.
	all := append([]*Manifest{root}, children...)
	for _, member := range all {
		if alive(member.ID) && member.Status.State != StateExited && member.Status.State != StateErrored && member.Status.State != StateStopped {
			return nil, UserError("agent_running", "a sandbox member still runs", "Attach to the member; do not resume a living sandbox.")
		}
	}
	if err := ValidateManifestStart(root); err != nil {
		return nil, err
	}
	if _, _, err := resumeArgv(root); err != nil {
		return nil, err
	}
	for _, child := range children {
		if _, err := ResumeSandboxMember(child, root); err != nil {
			return nil, err
		}
	}
	// The same P29/laptop structural validation runs before stop and again
	// after the old controller's last save, before any recreated wall starts.
	preview := CloneManifest(*root)
	if err := ApplyResumeLayers(&preview, a.Layers); err != nil {
		return nil, err
	}
	if err := ApplyRepositoryResume(&preview, a); err != nil {
		return nil, err
	}
	if err := prepareResumeEnvironment(&preview, a.Environment); err != nil {
		return nil, err
	}
	if err := validateResumeInputs(&preview, a.Inputs); err != nil {
		return nil, err
	}
	if err:=admitRestartMemoryRequestV1(operation.ownerContext,operation,a);err!=nil{return nil,err}
	debugOn := debugUntil(root, alive(root.ID)) != nil
	if err := stopHolder(root.ID, root); err != nil {
		return nil, UserError("busy", "the previous sandbox owner could not be stopped", "Wait for its owned cleanup before resuming.")
	}
	if oldHolderRuns(root.ID) {
		return nil, UserError("busy", "the previous sandbox owner still runs", "Wait for its cleanup before resuming.")
	}
	root, err = operation.LoadContext(operation.ownerContext)
	if err != nil {
		return nil, err
	}
	if err := CheckSandboxSnapshotLocked(original, root.ID, a.Snapshot); err != nil {
		return nil, err
	}
	if _, err := prepareResumeSecretDelivery(root, delivery, primaryValues); err != nil {
		return nil, err
	}
	if err := reconcileUnsavedLocked(root); err != nil {
		return nil, err
	}
	if err := ApplyResumeLayers(root, a.Layers); err != nil {
		return nil, err
	}
	if err := ApplyRepositoryResume(root, a); err != nil {
		return nil, err
	}
	switch {
	case a.MemoryMax > 0:
		root.Limits.MemoryAsked = a.MemoryMax
	case a.MemoryMax < 0:
		root.Limits.MemoryAsked = 0
	}
	if err := validateResumeInputs(root, a.Inputs); err != nil {
		return nil, err
	}
	if err := prepareResumeEnvironment(root, a.Environment); err != nil {
		return nil, err
	}
	root.HostLabel = a.HostLabel
	root.Inputs = CloneInputSummary(a.Inputs)
	root.Run.RespawnCount = 0
	members := make([]api.MemberResumeArgs, 0, len(children))
	for _, child := range children {
		members = append(members, api.MemberResumeArgs{ID: child.ID, Secrets: childValues[child.ID], SecretDelivery: childDeliveries[child.ID]})
	}
	_, why, err := startAgain(root, again{operation:operation,label: resumedLabel, reason: "sandbox sessions started again", secrets: primaryValues, secretDelivery: delivery, members: members, allowMissing: true})
	if err != nil {
		if refused:=restartRefusal(err);refused!=nil{return nil,refused}
		return nil, UserError("start_failed", "the complete sandbox could not start: "+err.Error(), "Check the supervisor log before trying again.")
	}
	selected, err := LoadManifest(original)
	if err != nil {
		return nil, err
	}
	if row := waitStarted(original, 2*time.Second); row != nil {
		selected = row
	}
	if original != root.ID {
		why = fmt.Sprintf("restarted its sandbox with %d sessions", len(all))
	}
	// This same primary operation still owns the complete resume publication.
	publicationRoot, err := LoadManifest(root.ID)
	if err != nil {
		return nil, err
	}
	result := &ResumeResult{Session: publicationInfoFrom(selected, alive(original), publicationRoot), Why: why, DebugStopped: debugOn}
	if err := checkSecretPublicationResponse(result); err != nil {
		return nil, err
	}
	return result, nil
}

// CheckResumeMemberValues is the same strict complete-ID/name check used at
// final host resume and the protected startup handoff, with no independent store.
func CheckResumeMemberValues(primary string, children []*Manifest, values []api.MemberResumeArgs) (map[string]map[string]api.Secret, error) {
	return checkedResumeMembers(primary, children, values)
}
