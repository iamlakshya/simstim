package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type purgeRootHistoryV1 struct {
	exists    bool
	reference cachefs.Reference
	sha256    string
}

// A selection restricts one original purge; history never creates a root grant.
// The actual fixed executor separately checks source, tier and native Start.
type purgeRootSelectionV1 struct {
	purge   *purgeOperationV1
	source  hostStateSourceWitness
	history purgeRootHistoryV1
	ids     []RootStepID
	script  RootScript
	empty   bool
}

func capturePurgeRootHistoryV1(ctx context.Context, p *purgeOperationV1) (result purgeRootHistoryV1, returned error) {
	if p == nil || ctx == nil {
		return result, rootStepError("root history has no original purge lifetime")
	}
	if err := p.check(ctx, p.conn); err != nil {
		return result, err
	}
	ctx = WithHostStateObservation(ctx, p.observation)
	returned = withStateTransaction(ctx, []string{"hosts", HostFileKey(p.receipt.Original.Name)}, "root-steps.json", false, false, func(tx *stateFileTxn) error {
		view := tx.view()
		result.exists = view.Exists
		if !view.Exists {
			return nil
		}
		if view.Size < 1 || view.Size > 4<<20 {
			return rootStepError("the original root history exceeds its fixed inspection bound")
		}
		reference, err := cachefs.CaptureFile(tx.root, tx.name, tx.source, 0o600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, io.NewSectionReader(view.ReaderAt, 0, view.Size)); err != nil {
			return err
		}
		if err := reference.CheckFile(tx.root, tx.name, tx.source, 0o600); err != nil {
			return err
		}
		result.reference = reference
		result.sha256 = hex.EncodeToString(hash.Sum(nil))
		return tx.check()
	})
	return result, returned
}

func samePurgeRootHistoryV1(a, b purgeRootHistoryV1) bool {
	if a.exists != b.exists {
		return false
	}
	if !a.exists {
		return true
	}
	return a.reference.Known() && b.reference.Known() && a.reference.Identity == b.reference.Identity && a.reference.Mode == b.reference.Mode && a.reference.Size == b.reference.Size && a.reference.Modified.Equal(b.reference.Modified) && a.sha256 == b.sha256
}

func selectPurgeRootStepsV1(records []RootStepRecord, hostID string) ([]RootStepID, error) {
	if !ValidHostID(hostID) {
		return nil, rootStepError("root undo has no original checked host identity")
	}
	seen := map[RootStepID]bool{}
	var ids []RootStepID
	for _, record := range records {
		if !validRootRecord(record) {
			return nil, rootStepError("the original root history has an unknown record")
		}
		if record.HostID != hostID || record.Mode != RootRun || record.Status != "running" && record.Status != "failed" && record.Status != "confirmed" {
			continue
		}
		for _, id := range record.Steps {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func readPurgeRootSelectionV1(ctx context.Context, p *purgeOperationV1) (purgeRootHistoryV1, []RootStepID, error) {
	before, err := capturePurgeRootHistoryV1(ctx, p)
	if err != nil {
		return before, nil, err
	}
	var ids []RootStepID
	owned := WithHostStateObservation(ctx, p.observation)
	err = withRootStepState(owned, p.receipt.Original, false, func(file *rootStepFile) error {
		if file.future {
			return rootStepError("future root history was preserved instead of selecting an undo")
		}
		var err error
		ids, err = selectPurgeRootStepsV1(file.Records, p.receipt.HostID)
		return err
	})
	if err != nil {
		return before, nil, err
	}
	after, err := capturePurgeRootHistoryV1(ctx, p)
	if err != nil {
		return before, nil, err
	}
	if !samePurgeRootHistoryV1(before, after) {
		return before, nil, rootStepError("the original root history changed while selecting its undo")
	}
	return before, ids, nil
}

func hasPurgeRootUndoStepsV1(ctx context.Context, p *purgeOperationV1) (bool, error) {
	_, ids, err := readPurgeRootSelectionV1(ctx, p)
	return len(ids) != 0, err
}

func preparePurgeRootUndoSelectionV1(ctx context.Context, p *purgeOperationV1, user string, uid int) (*purgeRootSelectionV1, error) {
	history, ids, err := readPurgeRootSelectionV1(ctx, p)
	if err != nil {
		return nil, err
	}
	selection := &purgeRootSelectionV1{purge: p, source: p.source.clone(), history: history, ids: slices.Clone(ids), empty: len(ids) == 0}
	if selection.empty {
		return selection, nil
	}
	applyIDs := slices.DeleteFunc(slices.Clone(ids), func(id RootStepID) bool { return id == RootNitroSlots })
	base, err := BuildRootScript(user, uid, applyIDs, 0)
	if err != nil {
		return nil, err
	}
	if slices.Contains(ids, RootNitroSlots) {
		step, _ := describeRootStep(RootNitroSlots, user, uid)
		step.Line = "Keep the recorded Nitro slot donation until reboot"
		base.Steps = append(base.Steps, step)
	}
	undo, err := filterPurgeRootUndoV1(base.Undo, ids)
	if err != nil {
		return nil, err
	}
	script := RootScript{User: user, Steps: slices.Clone(base.Steps), Undo: undo, Text: undo + "\nprintf '%s\\n' __ROOT_STEP_DONE__\n"}
	digest := sha256.Sum256([]byte(script.Text))
	script.SHA256 = hex.EncodeToString(digest[:])
	selection.script = script
	return selection, nil
}

func filterPurgeRootUndoV1(template string, ids []RootStepID) (string, error) {
	anchor := "    step=$(cat \"$j/id\")\n"

	if len(ids) == 0 || strings.Count(template, anchor) != 1 {
		return "", rootStepError("the fixed root undo has no unique closed selection anchors")
	}
	patterns := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, known := describeRootStep(id, "fixture", 1000); !known {
			return "", rootStepError("the root undo selected an unknown fixed step")
		}
		patterns = append(patterns, ShellQuote(string(id)))
	}
	filter := "    case \"$step\" in\n        " + strings.Join(patterns, "|") + ") ;;\n        *) printf 'Left root step %s: it has no original local run record for this host.\\n' \"$step\" >&2; continue ;;\n    esac\n"
	result := strings.Replace(template, anchor, anchor+filter, 1)
	// Helper files and journals without a native creator receipt are retained.
	// A selected step can undo only its own checked journal entry above.
	return retainPurgeRootHelpersV1(result)

}

func (selection *purgeRootSelectionV1) check(ctx context.Context, p *purgeOperationV1) error {
	if selection == nil || p == nil || selection.purge != p || !sameReceiptSource(selection.source, p.source) {
		return grantOperationError()
	}
	if err := p.check(ctx, p.conn); err != nil {
		return err
	}
	current, ids, err := readPurgeRootSelectionV1(ctx, p)
	if err != nil {
		return err
	}
	if !samePurgeRootHistoryV1(selection.history, current) || !slices.Equal(selection.ids, ids) {
		return rootStepError("the original root undo history or selected steps changed")
	}
	if selection.empty {
		return nil
	}
	data, err := json.Marshal(selection.script)
	if err != nil {
		return err
	}
	if len(data) > 256<<10 {
		return rootStepError("the fixed selected root undo exceeds its inspection bound")
	}
	return errors.Join(ctx.Err(), p.check(ctx, p.conn))
}

func retainPurgeRootHelpersV1(template string) (string, error) {
	lines := strings.SplitAfter(template, "\n")
	found := -1
	for index := 0; index+2 < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != `rm -f "$d/applied.json" "$d/undo.sh" "$d/subid-allocate.sh"` {
			continue
		}
		if strings.TrimSpace(lines[index+1]) != `rmdir "$d/journal" "$d"` || strings.TrimSpace(lines[index+2]) != `printf '%s\n' __ROOT_STEP_UNDONE__` {
			return "", rootStepError("the fixed root finaliser has an unknown helper removal sequence")
		}
		if found != -1 {
			return "", rootStepError("the fixed root finaliser has ambiguous helper removal anchors")
		}
		found = index
	}
	if found < 0 {
		return "", rootStepError("the fixed root finaliser has no closed helper removal anchor")
	}
	indent := lines[found][:len(lines[found])-len(strings.TrimLeft(lines[found], " \t"))]
	lines[found] = indent + "printf '%s\\n' 'Left the root journal helpers for the machine owner to inspect.' >&2\n"
	lines[found+1] = indent + "printf '%s\\n' __ROOT_STEP_LEFT__\n"
	lines[found+2] = ""
	return strings.Join(lines, ""), nil
}
