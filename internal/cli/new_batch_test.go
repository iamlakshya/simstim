package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

func countedCommand() *cobra.Command {
	c := &cobra.Command{Use: "new"}
	c.SetContext(context.Background())
	c.Flags().Int("count", 1, "")
	_ = c.Flags().Set("count", "4")
	return c
}

func TestCountedNewContinuesAfterRefusalAndPrintsOneArray(t *testing.T) {
	cmd := countedCommand()
	name, detach := "job", false
	var batch []map[string]any
	var names []string
	refused := &client.UserError{Msg: "the host has no room", Hint: "Stop another sandbox.", Code: "no_room", Kind: client.KindRefused}
	var runErr error
	out, stderr := captureOutput(t, func() {
		runErr = runCountedNew(cmd, []string{"box"}, 4, &name, &detach, true, &batch, func(*cobra.Command, []string) error {
			names = append(names, name)
			if !detach {
				t.Fatal("a counted run tried to attach")
			}
			if name == "job-2" {
				return refused
			}
			batch = append(batch, map[string]any{"name": name, "id": name})
			return nil
		})
	})
	if !slices.Equal(names, []string{"job-1", "job-2", "job-3", "job-4"}) || stderr != "" {
		t.Fatalf("starts=%v stderr=%q", names, stderr)
	}
	var rows []struct {
		V     int        `json:"v"`
		Name  string     `json:"name"`
		Error *errorJSON `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 4 {
		t.Fatalf("batch output=%q err=%v", out, err)
	}
	for i, row := range rows {
		if row.V != 1 || row.Name != names[i] {
			t.Fatalf("row %d=%+v", i, row)
		}
	}
	if rows[1].Error == nil || rows[1].Error.Code != "no_room" || rows[1].Error.Exit != 5 {
		t.Fatalf("refused row=%+v", rows[1])
	}
	var quiet *quietError
	if !errors.As(runErr, &quiet) || quiet.Kind != client.KindRefused {
		t.Fatalf("batch exit did not retain first refusal: %v", runErr)
	}
}

func TestCountedNewWithNoStartsKeepsStdoutEmpty(t *testing.T) {
	cmd := countedCommand()
	name, detach := "job", false
	var batch []map[string]any
	want := &client.UserError{Msg: "the host has no room", Hint: "Stop another sandbox.", Code: "no_room", Kind: client.KindRefused}
	var got error
	out, stderr := captureOutput(t, func() {
		got = runCountedNew(cmd, nil, 4, &name, &detach, true, &batch, func(*cobra.Command, []string) error { return want })
	})
	if got != want || out != "" || stderr != "" || len(batch) != 4 {
		t.Fatalf("failed batch: err=%v stdout=%q stderr=%q rows=%d", got, out, stderr, len(batch))
	}
}

func TestCountedNewRejectsZeroBeforeAnyStart(t *testing.T) {
	cmd := countedCommand()
	name, detach := "job", false
	var batch []map[string]any
	err := runCountedNew(cmd, nil, 0, &name, &detach, false, &batch, func(*cobra.Command, []string) error { t.Fatal("zero count reached admission"); return nil })
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "bad_count" || ue.Kind != client.KindUsage {
		t.Fatalf("zero count=%v", err)
	}
}

func TestCountedNewNeverReusesAnotherStartsRequestContext(t *testing.T) {
	type requestKey struct{}
	cmd := countedCommand()
	original := cmd.Context()
	name, detach := "job", false
	var batch []map[string]any
	starts := 0
	err := runCountedNew(cmd, nil, 2, &name, &detach, false, &batch, func(c *cobra.Command, _ []string) error {
		if c.Context().Value(requestKey{}) != nil {
			t.Fatal("another start's request or approval context was reused")
		}
		starts++
		c.SetContext(context.WithValue(c.Context(), requestKey{}, starts))
		batch = append(batch, map[string]any{"name": name})
		return nil
	})
	if err != nil || starts != 2 || cmd.Context() != original || name != "job" || detach {
		t.Fatalf("independent starts=%d err=%v name=%s detach=%t", starts, err, name, detach)
	}
}
