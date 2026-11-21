package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const gitOpPurgeInspectV1 = "purge.inspect"

type purgeGitInspectionV1 struct {
	Dirty    int `json:"dirty"`
	Unpushed int `json:"unpushed"`
}

func init() {
	gitOpKinds = append(gitOpKinds, &gitOpKind{gitOpPurgeInspectV1, time.Minute, func(request gitOpRequest, _ func(*GitInfo)) (gitOpAnswer, error) {
		if err := gitVersionGate(); err != nil {
			return gitOpAnswer{}, err
		}
		if request.Git == nil || !filepath.IsAbs(request.Git.Worktree) || filepath.Clean(request.Git.Worktree) != request.Git.Worktree {
			return gitOpAnswer{}, UserError("unsaved", "the worktree cannot be inspected safely", "Check the original session's worktree before removing the host.")
		}
		dirty, err := git(request.Git.Worktree, "status", "--porcelain")
		if err != nil {
			return gitOpAnswer{}, err
		}
		unpushed, err := git(request.Git.Worktree, "rev-list", "--count", "HEAD", "--not", "--remotes")
		if err != nil {
			return gitOpAnswer{}, err
		}
		count, err := strconv.Atoi(strings.TrimSpace(unpushed))
		if err != nil || count < 0 {
			return gitOpAnswer{}, UserError("unsaved", "git returned an unknown unpushed commit count", "Inspect and save the worktree before removing the host.")
		}
		return gitOpAnswer{Purge: &purgeGitInspectionV1{Dirty: countLines(dirty), Unpushed: count}}, nil
	}})
}

func inspectPurgeGitV1(ctx context.Context, git *GitInfo) (*purgeGitInspectionV1, error) {
	answer, err := gitOpWithinContext(ctx, findGitOp(gitOpPurgeInspectV1), gitOpRequest{Git: git}, time.Minute)
	if err != nil {
		return nil, err
	}
	if answer.Purge == nil || answer.Purge.Dirty < 0 || answer.Purge.Unpushed < 0 {
		return nil, UserError("unsaved", "the worktree inspection returned no complete result", "Save the worktree before removing the host.")
	}
	return answer.Purge, nil
}

func (owner *purgeOwnerV1) precheck() error {
	for _, done := range owner.report.Done {
		if done == "sessions" {
			return nil
		}
	}
	if owner.args.Force {
		if len(owner.report.Done) != 0 {
			return nil
		}
		return owner.completePhase("precheck")
	}
	manifests, err := trustedSessionManifests()
	if err != nil {
		return err
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	var unsaved []api.PurgeSession
	for _, observed := range manifests {
		if observed.SandboxOf != "" {
			continue
		}
		operation, err := BeginSessionOperation(owner.ctx, observed.ID, time.Second)
		if err != nil {
			return err
		}
		current, err := operation.LoadContext(owner.ctx)
		if err == nil {

			if current.CopiesPending {
				unsaved = append(unsaved, api.PurgeSession{ID: current.ID, Name: current.Name, Reason: "sandbox commits have not been pulled"})
			}
			if current.Status.State == StateWorking {
				unsaved = append(unsaved, api.PurgeSession{ID: current.ID, Name: current.Name, Reason: "the session reports that it is working"})
			}
			if current.Git != nil && current.Wall == nil {
				// Refusing an unavailable inspection needs no cleanliness proof.
				// Collect it before ANY session is stopped or data is removed.
				unsaved = append(unsaved, api.PurgeSession{ID: current.ID, Name: current.Name, Reason: ordinaryWorkInspectionUnavailableV1})
			} else if current.Git != nil {
				inspection, readErr := inspectPurgeGitV1(owner.ctx, current.Git)
				if readErr != nil {
					unsaved = append(unsaved, api.PurgeSession{ID: current.ID, Name: current.Name, Reason: "the original worktree could not be checked"})
				} else if inspection.Dirty != 0 || inspection.Unpushed != 0 {
					unsaved = append(unsaved, api.PurgeSession{ID: current.ID, Name: current.Name, Reason: fmt.Sprintf("%d changed files and %d unpushed commits", inspection.Dirty, inspection.Unpushed)})
				}
			}
		}
		closeErr := operation.CloseWithError()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if len(unsaved) == 0 {
		if len(owner.report.Done) != 0 {
			return nil
		}
		return owner.completePhase("precheck")
	}
	owner.report.Unsaved = unsaved
	if err := owner.gate(func(control *placementControl, _ *os.Root) error {
		if control.Destructive {
			return nil
		}
		// This refusal withdraws only THIS purge mark. The separate user
		// drain file stays unchanged and still controls ordinary admission.
		control.Drain, control.Nonce, control.Phase = "", "", ""
		control.Report = nil
		return nil
	}); err != nil {
		return err
	}
	error := UserError("unsaved", "host removal found work that has not been saved or pulled", "Save the listed work, or use --force after reviewing what will be lost.")
	error.Data, _ = json.Marshal(unsaved)
	return error
}

func (owner *purgeOwnerV1) completePhase(phase string) error {
	return owner.gate(func(control *placementControl, _ *os.Root) error {
		order := []string{"precheck", "sessions", "footprints", "roots", "helpers", "host-complete"}
		index := len(owner.report.Done)
		if index >= len(order) || order[index] != phase {
			return placementError("the purge phase differs from its original durable prefix")
		}
		owner.report.Done = append(owner.report.Done, phase)
		control.Phase = phase
		if phase != "precheck" {
			control.Destructive = true
		}
		return nil
	})
}
