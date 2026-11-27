package egress

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestJudgeSourceRetirementUsesActualAdditiveMintAndSameKindNewNameProducers(t *testing.T) {
	for _, producer := range []string{"mint", "new-name"} {
		t.Run(producer, func(t *testing.T) {
			server, capability := judgeCapabilityFixture(t)
			before := server.currentMembers()
			started := make(chan struct{})
			capability.testRoundTrip = func(request *http.Request) (*http.Response, error) {
				close(started)
				<-request.Context().Done()
				return nil, errors.New("cancelled")
			}
			value := api.Secret("EFGH2222")
			if producer == "mint" {
				value = api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
			}
			ctx := judgeSourceContext(t, server)
			callDone := make(chan error, 1)
			go func() { _, err := capability.Complete(ctx, []byte(`{"task":"`+string(value)+`"}`)); callDone <- err }()
			<-started
			changeDone := make(chan error, 1)
			go func() {
				if producer == "mint" {
					_, err := server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), value, "openai-api-key")
					changeDone <- err
					return
				}
				intent := memberIntent(before.generation, map[string]api.Secret{"ADDED_KEY": value}, "shell")
				intent.Binds["ADDED_KEY"] = api.SecretBind{Hosts: []string{"other.example"}, Header: "Authorization", Format: "Bearer {value}"}
				_, err := server.addBoundControlMember(memberJSON(t, intent))
				changeDone <- err
			}()
			select {
			case err := <-changeDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("actual source producer did not join its affected judge call")
			}
			select {
			case err := <-callDone:
				if err == nil {
					t.Fatal("retired-source judge returned success")
				}
			case <-time.After(time.Second):
				t.Fatal("retired judge retained physical IO")
			}
			if server.currentMembers() == before || len(server.flows.snapshot()) != 0 {
				t.Fatal("source producer failed to replace its immutable view or retained a judge call")
			}
			if producer == "mint" && server.currentMembers().generation != before.generation {
				t.Fatal("judge retirement changed additive mint primary authority semantics")
			}
		})
	}
}

func TestJudgeRetirementRejectsUnjoinableRecordBeforeCancellingOtherOwners(t *testing.T) {
	server, _ := judgeCapabilityFixture(t)
	state := server.currentMembers()
	cancelled := false
	server.flows.addHTTPRequest(activeFlow{judgeSource: state, cancel: func() { cancelled = true }})
	if err := server.retireJudgeSourcesLocked(t.Context(), nil); err == nil || cancelled {
		t.Fatal("unjoinable judge source retirement changed an owner before full validation")
	}
}
