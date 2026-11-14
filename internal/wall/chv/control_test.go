package chv

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunnerControlOwnExitAndClosedOperations(t *testing.T) {
	ours, theirs := net.Pipe()
	control := newRunnerControl(ours)
	defer control.close()
	allowExit := make(chan struct{})
	go func() {
		defer theirs.Close()
		encoder := json.NewEncoder(theirs)
		_ = encoder.Encode(runnerMessage{Kind: "ready"})
		var request runnerRequest
		if json.NewDecoder(theirs).Decode(&request) != nil {
			return
		}
		_ = encoder.Encode(runnerMessage{Kind: "reply", ID: request.ID + 1, State: "wrong"})
		_ = encoder.Encode(runnerMessage{Kind: "reply", ID: request.ID, State: "Running"})
		<-allowExit
		_ = encoder.Encode(runnerMessage{Kind: "exit", Exit: &runnerExit{Code: 17, Signal: "terminated", Error: "owned VMM ended"}})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := control.waitReady(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := control.call(ctx, "create"); err == nil {
		t.Fatal("arbitrary VMM API operation exposed")
	}
	state, err := control.call(ctx, "state")
	if err != nil || !strings.Contains(string(state), "Running") {
		t.Fatalf("state %s: %v", state, err)
	}
	close(allowExit)
	control.finish()
	exit := control.processExit()
	if exit.Code != 17 || exit.Signal != "terminated" || exit.Err == nil {
		t.Fatalf("actual owned exit was lost: %+v", exit)
	}
}

func TestRunnerControlCancellationInterruptsWriteAndWinsReady(t *testing.T) {
	ours, theirs := net.Pipe()
	defer theirs.Close()
	control := newRunnerControl(ours)
	defer control.close()
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { _, err := control.call(ctx, "state"); ended <- err }()
	cancel()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled control write remained blocked")
	}
	close(control.ready)
	if err := control.waitReady(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ready defeated cancellation: %v", err)
	}
}

func TestRunnerControlRejectsMalformedAndMissingOwnedResult(t *testing.T) {
	for _, data := range []string{"not-json\n", "{\"kind\":\"exit\"}\n", "{\"kind\":\"arbitrary\"}\n", "{\"kind\":\"ready\",\"injected\":true}\n", ""} {
		t.Run(data, func(t *testing.T) {
			ours, theirs := net.Pipe()
			control := newRunnerControl(ours)
			defer control.close()
			go func() { _, _ = theirs.Write([]byte(data)); theirs.Close() }()
			control.finish()
			if control.processExit().Err == nil {
				t.Fatal("unowned success inferred from helper EOF")
			}
		})
	}
}
