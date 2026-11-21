package egress

import "testing"

func TestModelUsageTerminalRequiresCompleteForwardedLogicalEvent(t *testing.T) {
	meter, turn := usageTurnFixture(t, "openai-responses", "model-a", 4)
	turn.ResponseV1(ModelUsageWebSocketV1)
	turn.ForwardedV1([]byte(`{"type":"response.output_text.delta","delta":"unfinished"}`))
	if turn.TerminalV1() {
		t.Fatal("a content event ended the logical response")
	}
	terminal := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`)
	for _, value := range terminal[:len(terminal)-1] {
		turn.ForwardedV1([]byte{value})
		if turn.TerminalV1() {
			t.Fatal("an incomplete forwarded terminal frame ended the response")
		}
	}
	turn.ForwardedV1(terminal[len(terminal)-1:])
	if !turn.TerminalV1() {
		t.Fatal("complete actual terminal event was omitted")
	}
	first := turn.FinishV1(true)
	second := turn.FinishV1(true)
	if first != second || first.Estimated || first.Counts.TotalTokens != 3 || meter.SnapshotV1().Total.Requests != 1 {
		t.Fatal("terminal observation duplicated or changed accounting", first, second)
	}
}

func TestModelUsageTerminalSSEWaitsForTheActualEventBoundary(t *testing.T) {
	_, turn := usageTurnFixture(t, "anthropic-messages", "model-a", 0)
	turn.ResponseV1(ModelUsageSSEV1)
	turn.ForwardedV1([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n"))
	turn.ForwardedV1([]byte("data: {\"type\":\"message_stop\"}\n"))
	if turn.TerminalV1() {
		t.Fatal("a buffered SSE event became a terminal observation")
	}
	turn.ForwardedV1([]byte("\n"))
	if !turn.TerminalV1() {
		t.Fatal("forwarded SSE terminal boundary was omitted")
	}
	if got := turn.FinishV1(true); got.Estimated || got.Counts.TotalTokens != 7 {
		t.Fatal("terminal observation changed cumulative usage", got)
	}
	var absent *ModelUsageTurnV1
	if absent.TerminalV1() || (&ModelUsageTurnV1{ignored: true}).TerminalV1() {
		t.Fatal("absent accounting invented a response boundary")
	}
}
