package egress

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBoundSynchronousControlMutationCancelsMintWaitBeforeJoiningWithoutItsACK(t *testing.T) {
	for _, operation := range []string{CtlArm, CtlMemberAdd} {
		t.Run(operation, func(t *testing.T) {
			f := newBoundControlFixture(t, false)
			initial, err := f.server.initialiseBoundControl(memberJSON(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})))
			if err != nil {
				t.Fatal(err)
			}
			request := boundRequestScope(t, f.server, []string{"KEY"})
			callback := make(chan boundMintResult, 1)
			go func() {
				value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
				sentinel, err := f.server.registerResponseSecret(request.ctx, "example.com:443", boundPrimaryUse(f.server, "KEY"), value, "openai-api-key")
				// The physical request owner cannot finish until its actual
				// registry callback returns. Cancel alone does not close done.
				request.close()
				callback <- boundMintResult{sentinel, err}
			}()
			event := f.nextMint(t)
			var args any = ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}}
			if operation == CtlMemberAdd {
				args = memberIntent(initial.Generation, map[string]api.Secret{"KEY": "WXYZ1111"}, "codex")
			}
			// No receipt ACK is sent. The real sole control reader now enters
			// a synchronous arm/member handler and joins this request owner.
			if err := f.writer.WriteJSON(proto.TRequest, proto.Request{ID: 2, Op: operation, Args: memberJSON(t, args)}); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-f.responses:
				if response.ID != 2 || !response.OK {
					t.Fatal("control mutation failed instead of cancelling and joining its exact request")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("control mutation waited for ACK behind its own sole reader or the five-second receipt timeout")
			}
			select {
			case result := <-callback:
				if result.err == nil || result.sentinel != "" {
					t.Fatal("cancelled joined request emitted an undurable stand-in")
				}
			case <-time.After(time.Second):
				t.Fatal("mutation replied before the callback-owned finaliser actually completed")
			}
			if err := f.acknowledge(event, true); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-f.responses:
				if response.OK {
					t.Fatal("late ACK revived the previous request/cohort after control mutation")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("sole control reader did not resume after the request join")
			}
		})
	}
}
