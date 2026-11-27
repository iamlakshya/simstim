package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestHeldActionGrammarRejectsRawTargetAndUnrelatedGrantFlagsBeforeLookup(t *testing.T) {
	cliPolicyHome(t)
	id := strings.Repeat("a", 32)
	for _, args := range [][]string{
		{"policy", "allow", "api.example", "--request", id, "--in", "missing/hold2345"},
		{"policy", "allow", "--request", id, "--in", "missing/hold2345", "--tools", "files"},
		{"policy", "allow", "--request", id, "--in", "missing/hold2345", "--for", "1h"},
		{"policy", "allow", "--request", id, "--in", "missing/hold2345", "--public"},
		{"policy", "allow", "--request", id, "--in", "missing/hold2345", "--dry-run"},
		{"policy", "allow", "--request", id, "--in", "missing/named-session"},
		{"policy", "allow", "--request", id, "--in", "missing/hold2345", "--all"},
		{"policy", "allow", "--request", id, "--in", "missing/hold2345", "--repo", "."},
	} {
		cmd := Root()
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(client.WithoutInput(t.Context()))
		var user *client.UserError
		if !errors.As(err, &user) || user.Kind != client.KindUsage || user.Hint == "" {
			t.Fatal("held action escaped exact argument validation", args, err)
		}
	}
}

func TestBudgetPendingOutputKeepsReasonOpaqueIDAndOriginalExpiry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	request := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 2, Host: "upload.example", Port: 443,
		Opened: now, Expires: now.Add(time.Minute), Status: api.NetworkHoldOpen, Reason: api.NetworkHoldUploadBudget}
	var output bytes.Buffer
	if err := writePendingNetworkRequest(&output, request, now); err != nil || !strings.Contains(output.String(), "upload budget; once or refuse") || !strings.HasPrefix(output.String(), request.ID+"\t") || !strings.Contains(output.String(), request.Expires.Format(time.RFC3339)) {
		t.Fatal("budget output hid the reason or changed the original identity/lifetime", output.String(), err)
	}
	output.Reset()
	request.Reason = "future-budget"
	if err := writePendingNetworkRequest(&output, request, now); err == nil || output.Len() != 0 {
		t.Fatal("a future budget reason produced a current actionable row")
	}
}

func TestUploadBudgetUsesExistingTypedPolicySetting(t *testing.T) {
	for _, value := range []string{"0M", "16M", "32M"} {
		values, err := client.ParseLayerSets([]string{"network.upload_budget=" + value})
		if err != nil || values.Network.UploadBudget == nil || *values.Network.UploadBudget != value {
			t.Fatal("the existing upload budget setting lost its typed meaning", value, err)
		}
	}
	if _, err := client.ParseLayerSets([]string{"network.upload_budget=-1M"}); err == nil {
		t.Fatal("a negative upload budget became an exception grammar")
	}
}
