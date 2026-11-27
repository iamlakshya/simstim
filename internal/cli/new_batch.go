package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

// runCountedNew keeps admission and consent independent for every start.
func runCountedNew(cmd *cobra.Command, args []string, count int, name *string, detach *bool, asJSON bool, batch *[]map[string]any, runOne func(*cobra.Command, []string) error) error {
	if !cmd.Flags().Changed("count") {
		return runOne(cmd, args)
	}
	if count < 1 {
		return policyUsage("--count must be at least 1", "Pass a positive number of sessions.", "bad_count")
	}
	originalContext := cmd.Context()
	originalName, originalDetach := *name, *detach
	defer func() {
		cmd.SetContext(originalContext)
		*name, *detach = originalName, originalDetach
	}()
	if *name == "" {
		*name = "session"
	}
	if err := client.CheckSessionName(*name + "-" + fmt.Sprint(count)); err != nil {
		return err
	}
	*detach = true
	baseName := *name
	var first error
	started := 0
	for i := 1; i <= count; i++ {
		// Each start creates its own immutable grant request. A prior start's
		// closed request or terminal answer cannot approve another session.
		cmd.SetContext(originalContext)
		if err := originalContext.Err(); err != nil {
			if first == nil {
				first = err
			}
			break
		}
		*name = fmt.Sprintf("%s-%d", baseName, i)
		if err := runOne(cmd, args); err != nil {
			if first == nil {
				first = err
			}
			r := describe(err, cmd)
			*batch = append(*batch, map[string]any{"name": *name, "error": errorJSON{Code: r.code, Kind: r.kind.String(), Message: r.msg, Try: r.try, Exit: exitCode(r.kind), Suggestions: r.suggestions, Data: validJSONData(r.data)}})
		} else {
			started++
		}
	}
	if started == 0 {
		return first
	}
	if !asJSON {
		for _, item := range *batch {
			if e, ok := item["error"].(errorJSON); ok {
				fmt.Fprintf(os.Stderr, "%s: %s\n", shown(item["name"].(string)), shown(e.Message))
			}
		}
	}
	if asJSON {
		if err := printJSON(*batch); err != nil {
			return err
		}
	}
	if first != nil {
		r := describe(first, cmd)
		return &quietError{&client.UserError{Msg: r.msg, Hint: r.try, Code: r.code, Kind: r.kind}}
	}
	return nil
}
