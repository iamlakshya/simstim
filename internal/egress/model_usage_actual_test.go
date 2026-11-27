package egress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestActualModelObserverCountsOnlySuccessfulDecodedWritesAndKnownLiveQuota(t *testing.T) {
	meter := NewModelUsageMeterV1()
	turn, refused, err := meter.BeginV1(ModelUsageRequestV1{Wire: "anthropic-messages", Path: "/v1/messages", Model: "model-a", RequestBytes: 4}, 10)
	if err != nil || refused != nil {
		t.Fatal(err)
	}
	body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1000,\"output_tokens\":0}}}\n\n"
	response := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	var complete atomic.Bool
	configureModelUsageResponse(response, turn, &complete)
	out := httptest.NewRecorder()
	writer := &modelUsageHTTPWriter{ResponseWriter: out, turn: turn, active: true}
	readAhead, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if meter.BudgetRefusalV1("anthropic-messages", 10) != nil || meter.SnapshotV1().Total.Requests != 0 {
		t.Fatal("origin read-ahead was counted before a successful forwarded write")
	}
	if _, err := writer.Write(readAhead); err != nil {
		t.Fatal(err)
	}
	if out.Body.String() != body || !complete.Load() {
		t.Fatal("observer changed decoded forwarded bytes or EOF")
	}
	if _, refused, err := meter.BeginV1(ModelUsageRequestV1{Wire: "anthropic-messages", Path: "/v1/messages"}, 10); err != nil || refused == nil || refused.Used != 1000 {
		t.Fatal("reported live usage did not block a new logical request", err)
	}
	result := turn.FinishV1(false)
	estimate, _ := modelUsageEstimateV1(4, uint64(len(body)))
	if !result.Estimated || result.Counts.TotalTokens != estimate {
		t.Fatal("cut response changed the documented byte estimate")
	}
	snapshot := meter.SnapshotV1()
	if snapshot.Total.TotalTokens != estimate || snapshot.BudgetTokens != 1000 || !api.ValidModelUsageV1(snapshot) {
		t.Fatal("cut response discarded its known charge floor or corrupted exact grand totals")
	}
	if _, refused, _ := meter.BeginV1(ModelUsageRequestV1{Wire: "anthropic-messages", Path: "/v1/messages"}, 10); refused == nil {
		t.Fatal("cutting an answer reopened known exhausted quota")
	}
}

func TestActualModelSnapshotScrubsTheWholePrivateLabelBeforeItsDisplayCap(t *testing.T) {
	meter := NewModelUsageMeterV1()
	value := strings.Repeat("K", 256)
	finishReportedUsage(t, meter, value, 5)
	scrubber := NewScrubber(map[string]string{"KEY": value})
	snapshot := meter.snapshotWithSourceV1(scrubber.snapshot())
	if len(snapshot.Models) != 1 || snapshot.Models[0].Model != "[KEY]" || !snapshot.Models[0].Redacted || strings.Contains(snapshot.Models[0].Model, "KK") {
		t.Fatal("raw private model label was cut before current Source redaction")
	}
}

func TestActualPrivateJudgeModelUsesTheSameLiveMeter(t *testing.T) {
	args := trustedRoleArgs(t, false)
	server, _ := newBoundRegistryServer(t, args)
	endpoint, request, _, err := judgeEndpoint(*args.Initial.Policy.Classifier.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	capability := &judgeModelCapability{server: server, endpoint: endpoint, request: request, settings: args.Initial.Policy.Classifier, key: "KEY", testRoundTrip: func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer ABCD0000" {
			t.Fatal("actual private role header missing")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"total_tokens":5}}`))}, nil
	}}
	server.policyMu.Lock()
	ctx := server.withJudgeSourceSnapshotLocked(context.Background())
	server.policyMu.Unlock()
	if _, err := capability.Complete(ctx, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	snapshot := server.modelUsageSnapshot()
	if snapshot.Total.Requests != 1 || snapshot.Total.TotalTokens != 5 || snapshot.Scope != "sandbox" {
		t.Fatal("private optional judge usage escaped the actual root meter")
	}
}

func TestActualModelUsagePublicationRedactsAQueuedLabelAtItsOwnedWriterTurn(t *testing.T) {
	server, _, stream, _, _ := exportFixture(t, false)
	value := strings.Repeat("K", 256)
	finishReportedUsage(t, server.usage, value, 5)
	server.ctlMu.Lock()
	finished := make(chan error, 1)
	go func() { finished <- server.writeModelUsageSnapshot(t.Context(), server.ctl, proto.Request{ID: 1}) }()
	server.policyMu.Lock()
	args, err := server.currentMembers().initialArgs()
	if err != nil {
		t.Fatal(err)
	}
	args.Secrets["NEW"] = value
	next, err := prepareInitialState(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.retireJudgeSourcesLocked(t.Context(), next.state); err != nil {
		t.Fatal(err)
	}
	server.publishControlState(next, false)
	server.policyMu.Unlock()
	server.ctlMu.Unlock()
	response, raw := readExport(t, stream)
	var usage api.ModelUsageV1
	if !response.OK || json.Unmarshal(response.Result, &usage) != nil || len(usage.Models) != 1 || usage.Models[0].Model != "[NEW]" || strings.Contains(string(raw), strings.Repeat("K", 96)) {
		t.Fatal("queued usage response disclosed a newly known model-label prefix")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
