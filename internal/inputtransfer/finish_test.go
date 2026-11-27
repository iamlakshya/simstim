package inputtransfer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type completeObservedStream struct {
	Stream
	complete chan struct{}
	once     sync.Once
}

func (s *completeObservedStream) Write(data []byte) (int, error) {
	frame, err := proto.Read(bufio.NewReader(bytes.NewReader(data)))
	if err == nil && frame.Type == proto.TEvent {
		var message event
		if json.Unmarshal(frame.Payload, &message) == nil && message.Event == "input.complete" {
			s.once.Do(func() { close(s.complete) })
		}
	}
	return s.Stream.Write(data)
}

func TestClientCompleteWaitsForOwnerFinishAndFailedSaveCannotClaimIt(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable finish", true: "failed save"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			data := []byte("fixture data")
			plan, scope := transferPlan(t, data)
			summary, _ := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
			options, _ := receiverFixture(t)
			options.Scope = scope
			client, rawClient := streamPair(t)
			toGuest, guest := streamPair(t)
			observed := &completeObservedStream{Stream: rawClient, complete: make(chan struct{})}
			spool, err := os.CreateTemp(t.TempDir(), "metadata-")
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			finishEntered, finishAllowed := make(chan struct{}), make(chan struct{})
			guestDone, forwardDone, sendDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
			go func() { guestDone <- completeFixture(ctx, guest, options) }()
			go func() {
				forwardDone <- Forward(ctx, observed, ForwardOptions{Scope: scope, Summary: summary, CopyMax: options.CopyMax, SeedMax: options.SeedMax,
					Spool: spool, OpenGuest: func(context.Context) (Stream, error) { return toGuest, nil },
					Finish: func(ctx context.Context) error {
						close(finishEntered)
						select {
						case <-finishAllowed:
						case <-ctx.Done():
							return ctx.Err()
						}
						if fail {
							return errors.New("fixture durable save failed")
						}
						return spool.Close()
					}})
			}()
			go func() {
				sendDone <- Send(ctx, client, scope, plan, Sources{File: func(_ context.Context, _ api.InputFile, out io.Writer) error {
					_, err := out.Write(data)
					return err
				}}, options.CopyMax, options.SeedMax)
			}()
			select {
			case <-finishEntered:
			case <-ctx.Done():
				t.Fatal("owner finish was never reached")
			}
			select {
			case <-observed.complete:
				t.Fatal("client completion preceded owner close/save")
			default:
			}
			close(finishAllowed)
			sendErr, forwardErr := <-sendDone, <-forwardDone
			if fail {
				if sendErr == nil || forwardErr == nil {
					t.Fatal("failed owner save became client success")
				}
				select {
				case <-observed.complete:
					t.Fatal("failed save emitted client complete")
				default:
				}
			} else if sendErr != nil || forwardErr != nil {
				t.Fatalf("successful finish = %v / %v", sendErr, forwardErr)
			}
			if err := <-guestDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
