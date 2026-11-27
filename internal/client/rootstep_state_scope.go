package client

import (
	"context"
	"io"

	"github.com/iamlakshya/simstim/internal/api"
)

type rootStepRecordProofKey struct{}

func checkRootStepRecordPurpose(ctx context.Context, h Host, record RootStepRecord) error {
	if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	intent, execution := r.root, r.rootExecution
	r.mu.Unlock()
	if intent == nil || !SameEntry(intent.conn.Host, h) || record.HostID != intent.conn.HostID() || record.SHA256 != intent.script.SHA256 || record.Mode != intent.mode || len(record.Steps) != len(intent.script.Steps) {
		return grantPurposeError()
	}
	for index, id := range record.Steps {
		if id != intent.script.Steps[index].ID {
			return grantPurposeError()
		}
	}
	if record.Mode == RootPrint && record.Status == "printed" || record.Mode == RootSkip && record.Status == "skipped" {
		return nil
	}
	proof, _ := ctx.Value(rootStepRecordProofKey{}).(*rootStepExecution)
	if proof == nil || proof != execution || proof.intent != intent {
		return grantPurposeError()
	}
	proof.mu.Lock()
	defer proof.mu.Unlock()
	valid := record.Status == "running" && proof.attempted && proof.starting && !proof.done || record.Status == "failed" && proof.attempted && proof.done && !proof.verified ||
		record.Status == "confirmed" && proof.attempted && proof.done && proof.verified
	if !valid {
		return grantPurposeError()
	}
	return nil
}

func withRootStepRecordObservation(ctx context.Context, h Host, hostID string, run func(context.Context) error) error {
	r := grantRequestOf(ctx)
	if r == nil {
		return grantPurposeError()
	}
	r.mu.Lock()
	intent := r.root
	r.mu.Unlock()
	if intent == nil || !SameEntry(intent.conn.Host, h) || intent.conn.HostID() != hostID {
		return grantPurposeError()
	}
	owned, release, err := intent.conn.beginLocalGrant(ctx, grantLocalRoot, grantOpRootStep, "record original root-step attempt")
	if err != nil {
		return err
	}
	defer release()
	if err := intent.source.check(owned, intent.conn); err != nil {
		return err
	}
	return run(owned)
}

// This conservative read uses one actual FD cursor and returns no state or
// public-secret histories. It is never a root execution permission.
func localSandboxHistoryStarted(ctx context.Context, h Host, hostID string) (bool, error) {
	found := false
	err := withAgentPolicySnapshot(ctx, h, func(source io.ReaderAt, size int64, exists bool) error {
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
		matches, err := projection.StringEqual(fields[1], h.Name)
		if err != nil || !matches {
			return badSandboxState()
		}
		known := false
		for _, version := range []string{"1", "2", "3", "4"} {
			yes, err := projection.Literal(fields[0], version)
			if err != nil {
				return err
			}
			known = known || yes
		}
		if !known {
			return badSandboxState()
		}
		knownRow := api.ExactJSONFieldNames((*savedSandbox)(nil))
		rows, err := projection.Fields(fields[2])
		if err != nil {
			return err
		}
		for {
			row, exists, err := rows.Next()
			if err != nil {
				return err
			}
			if !exists {
				return nil
			}
			if err := projectedSessionID(projection, row.Name); err != nil {
				return err
			}
			values, err := projection.Fields(row.Value)
			if err != nil {
				return err
			}
			rowIDKnown := false
			for {
				value, exists, err := values.Next()
				if err != nil {
					return err
				}
				if !exists {
					break
				}
				canonical := false
				for _, name := range knownRow {
					yes, err := projection.StringEqual(value.Name, name)
					if err != nil {
						return err
					}
					canonical = canonical || yes
				}
				if !canonical {
					return badSandboxState()
				}
				if equal, err := projection.StringEqual(value.Name, "host_id"); err != nil {
					return err
				} else if equal {
					rowIDKnown = true
					matches, err := projection.StringEqual(value.Value, hostID)
					if err != nil {
						return err
					}
					found = found || matches
					empty, err := projection.StringEqual(value.Value, "")
					if err != nil {
						return err
					}
					found = found || empty
				}
			}
			if !rowIDKnown {
				found = true
			}
		}
	})
	return found, err
}
