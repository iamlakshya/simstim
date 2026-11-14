package onboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestDoneRejectsRawIDWithoutChangingFlow(t *testing.T) {
	for _, id := range []string{"", "valid1\u200b", "id; command", strings.Repeat("x", 65)} {
		for _, ended := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/%v", id, ended), func(t *testing.T) {
				f := NewFlow("box", nil, nil)
				if err := f.Choose("claude"); err != nil {
					t.Fatal(err)
				}
				a := f.Next()
				a.Session = id
				// Include private queues and notes in the snapshot, not only Items.
				before := fmt.Sprintf("%#v|%#v|%#v|%#v", *f, f.Items, f.notes, f.Log.Lines())
				err := f.Done(a, []api.AgentCheck{ready("claude", "hostile\x1btitle", api.LoginYes)}, ended)
				var ue *client.UserError
				if !errors.As(err, &ue) || ue.Code != "bad_session_id" || ue.Hint == "" {
					t.Fatalf("%v", err)
				}
				after := fmt.Sprintf("%#v|%#v|%#v|%#v", *f, f.Items, f.notes, f.Log.Lines())
				if before != after {
					t.Fatalf("bad ID changed flow:\n%s\n%s", before, after)
				}
			})
		}
	}
}

func TestFlowLineCleansNotesAndKeepsStrictIdentityHint(t *testing.T) {
	f := NewFlow("box", nil, nil)
	_ = f.Choose("claude")
	a := f.Next()
	a.Session = "legacy_ID"
	if err := f.Done(a, nil, false); err != nil {
		t.Fatal(err)
	}
	if hint := f.Line("claude").Try; !strings.Contains(hint, " attach --id -- box/legacy_ID") {
		t.Fatalf("ambiguous hint %q", hint)
	}
	f.notes["claude"] = "host\x1b]52;c;value\x07\u202e text"
	raw := f.notes["claude"]
	l := f.Line("claude")
	if strings.ContainsAny(l.Try, "\x1b\x07\u202e") || f.notes["claude"] != raw {
		b, _ := json.Marshal(l)
		t.Fatalf("unsafe line or changed raw note: %s", b)
	}
}
