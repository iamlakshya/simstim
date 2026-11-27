package alerts

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// DecodeStreamEvent is the one V1 record decoder for live streams and native
// audit replay. A target is an exact ID, never a name or permission witness.
func DecodeStreamEvent(raw []byte, target string) (Event, error) {
	bad := func(message string) (Event, error) {
		return Event{}, &Error{Msg: message, Hint: "Run events again with a current client and complete host helper."}
	}
	var event Event
	if len(raw) > proto.MaxFrame || json.Unmarshal(raw, &event) != nil || event.V != Version || !IsStreamKind(event.Event) {
		return bad("the source sent an invalid or unsupported event")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.Time); err != nil {
		return bad("the source sent an event without a valid time")
	}
	hostEvent := event.Event == HealthDegraded || event.Event == HealthRecovered
	if !api.ValidSessionID(event.Session.ID) && !(hostEvent && event.Session.ID == "") || target != "" && event.Session.ID != target {
		return bad("the event source changed its session target")
	}
	if event.Detail != "" || event.DetailFrom != "" {
		return bad("the event source sent unredacted detail")
	}
	if event.Stream != "" || event.Sequence != 0 {
		if _, err := StreamEventIdentity(event); err != nil {
			return Event{}, err
		}
	}
	return event, nil
}

// StreamEventIdentity identifies one published data record across live/replay.
// It is never accepted as lineage, creation evidence or grant authority.
func StreamEventIdentity(event Event) (string, error) {
	bad := &Error{Msg: "the native event has no valid replay identity", Hint: "Update the complete host helper, then read events again."}
	if len(event.Stream) != 32 || event.Sequence == 0 {
		return "", bad
	}
	raw, err := hex.DecodeString(event.Stream)
	if err != nil || len(raw) != 16 || hex.EncodeToString(raw) != event.Stream {
		return "", bad
	}
	return event.Stream + ":" + strconv.FormatUint(event.Sequence, 10), nil
}
