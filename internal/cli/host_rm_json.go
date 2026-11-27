package cli

import (
	"bytes"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type hostRemovalJSONV1 struct {
	Host         string           `json:"host"`
	Removed      bool             `json:"removed"`
	Destroyed    bool             `json:"destroyed"`
	LocalCleanup bool             `json:"local_cleanup,omitempty"`
	Purge        *hostPurgeJSONV1 `json:"purge,omitempty"`
}

type hostPurgeJSONV1 struct {
	MacLeaveNotice      string            `json:"mac_leave_notice,omitempty"`
	HostComplete        bool              `json:"host_complete"`
	GuardRemoved        bool              `json:"guard_removed"`
	HelperGone          bool              `json:"helper_gone"`
	Removed             []purgeItemJSONV1 `json:"removed"`
	Left                []purgeItemJSONV1 `json:"left"`
	Logins              []string          `json:"logins"`
	BaselineChanged     []string          `json:"baseline_changed"`
	BaselineUnavailable []string          `json:"baseline_unavailable"`
	Rotate              []string          `json:"rotate"`
	RootMode            string            `json:"root_mode"`
	RootUndoRan         bool              `json:"root_undo_ran"`
	RootOutput          string            `json:"root_output,omitempty"`
}

type purgeItemJSONV1 struct {
	Kind   string `json:"kind"`
	Count  uint64 `json:"count,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

func publicPurgeItemsV1(items []api.PurgeItem) []purgeItemJSONV1 {
	result := make([]purgeItemJSONV1, 0, len(items))
	for _, item := range items {
		result = append(result, purgeItemJSONV1{Kind: api.CleanText(item.Kind, 128), Count: item.Count, Path: api.CleanText(item.Path, 4096), Reason: api.CleanText(item.Reason, 4096), Hint: api.CleanText(item.Hint, 4096)})
	}
	return result
}

func publicPurgeStringsV1(items []string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, api.CleanText(item, 4096))
	}
	return result
}

func publicPurgeReportV1(report api.PurgeResult) *hostPurgeJSONV1 {
	return &hostPurgeJSONV1{HostComplete: report.HostComplete, GuardRemoved: report.GuardRemoved, HelperGone: report.HelperGone, Removed: publicPurgeItemsV1(report.Removed), Left: publicPurgeItemsV1(report.Left), Logins: publicPurgeStringsV1(report.Logins)}
}

type purgeRootOutputV1 struct{ bytes.Buffer }

func (output *purgeRootOutputV1) Write(data []byte) (int, error) {
	if output.Len() > 256<<10 || len(data) > 256<<10-output.Len() {
		return 0, &client.UserError{Msg: "the fixed root undo output exceeds its JSON report bound", Hint: "Run the same removal without --json to read its full output.", Code: "output_bound", Kind: client.KindRefused}
	}
	return output.Buffer.Write(data)
}
