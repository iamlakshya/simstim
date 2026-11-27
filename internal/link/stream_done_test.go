package link

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func streamDoneModes(t *testing.T, run func(*testing.T)) {
	t.Helper()
	for _, mode := range []string{"socketpair", "pipe"} {
		t.Run(mode, func(t *testing.T) {
			value := ""
			if mode == "pipe" {
				value = "1"
			}
			t.Setenv("LINK_TEST_PIPE", value)
			run(t)
		})
	}
}

func awaitStreamDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatal("actual stream lifecycle did not notify its observer")
	}
}

func requireStreamActive(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("a request half-close was reported as abandonment")
	default:
	}
}

func TestStreamDonePreservesExactHalfClosedRequestAndResponse(t *testing.T) {
	streamDoneModes(t, func(t *testing.T) {
		sup, guest := pair(t, Options{})
		request, responder := openPair(t, sup, guest)
		requestDone, responderDone := request.Done(), responder.Done()
		if request.Done() != requestDone || responder.Done() != responderDone {
			t.Fatal("lifecycle observer channel changed identity")
		}
		wantRequest := bytes.Repeat([]byte{0, 1, 2, 0x1b, '[', '?', 'A', 0xff}, 4096)
		if _, err := request.Write(wantRequest); err != nil {
			t.Fatal(err)
		}
		if err := request.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		gotRequest, err := io.ReadAll(responder)
		if err != nil || !bytes.Equal(gotRequest, wantRequest) || !responder.PeerWriteClosed() {
			t.Fatal("observed lifecycle changed request bytes or ordered half-close", err)
		}
		requireStreamActive(t, requestDone)
		requireStreamActive(t, responderDone)
		wantResponse := bytes.Repeat([]byte{0xff, 0, 'R', '\r', '\n', 0x1b, '[', 'm'}, 4096)
		if _, err := responder.Write(wantResponse); err != nil {
			t.Fatal(err)
		}
		if err := responder.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		gotResponse, err := io.ReadAll(request)
		if err != nil || !bytes.Equal(gotResponse, wantResponse) || !request.PeerWriteClosed() {
			t.Fatal("a write half-close cancelled the exact response", err)
		}
		requireStreamActive(t, requestDone)
		requireStreamActive(t, responderDone)
		if err := responder.Close(); err != nil {
			t.Fatal(err)
		}
		awaitStreamDone(t, requestDone)
		awaitStreamDone(t, responderDone)
		_ = request.Close()
	})
}

func TestStreamDoneObservesActualPeerFullCloseAndResetWithoutReading(t *testing.T) {
	streamDoneModes(t, func(t *testing.T) {
		for _, code := range []byte{closeBoth, closeRefused} {
			session, peer := scripted(t, Supervisor, Options{}, helloOf(Guest))
			peer.open(2, `{"kind":"egress"}`)
			stream, err := session.Accept(testCtx(t))
			if err != nil {
				t.Fatal(err)
			}
			done := stream.Done()
			requireStreamActive(t, done)
			peer.close(2, code)
			awaitStreamDone(t, done)
			if stream.Done() != done {
				t.Fatal("a terminal read-only observer changed identity")
			}
			_ = stream.Close()
		}
	})
}

func TestStreamDoneObservesSessionTerminationAndLogicalAbort(t *testing.T) {
	streamDoneModes(t, func(t *testing.T) {
		for _, abort := range []bool{false, true} {
			sup, guest := pair(t, Options{})
			stream, peer := openPair(t, sup, guest)
			done, peerDone := stream.Done(), peer.Done()
			if abort {
				sup.AbortStreams()
				awaitStreamDone(t, done)
			}
			_ = sup.Close()
			awaitStreamDone(t, done)
			awaitStreamDone(t, peerDone)
		}
	})
}

func TestStreamDoneLateObserversSeeExistingTerminalState(t *testing.T) {
	streamDoneModes(t, func(t *testing.T) {
		sup, guest := pair(t, Options{})
		stream, peer := openPair(t, sup, guest)
		if err := peer.Close(); err != nil {
			t.Fatal(err)
		}
		done := stream.Done()
		awaitStreamDone(t, done)
		if stream.Done() != done {
			t.Fatal("late lifecycle observation allocated a different channel")
		}
		_ = stream.Close()
		late, _ := openPair(t, sup, guest)
		_ = sup.Close()
		awaitStreamDone(t, late.Done())
	})
}
