package supervisor

import (
	"errors"
	"github.com/iamlakshya/simstim/internal/host"
	"testing"
)

func TestWarmIncomingOperationClosureJoinsActualWorkerCompletion(t *testing.T) {
	canceled := make(chan struct{})
	done := make(chan struct{})
	scope := &nativeStopScopeV1{warmCancel: func() {
		select {
		case <-canceled:
		default:
			close(canceled)
		}
	}, warmDone: done}
	closed := make(chan error, 1)
	go func() { closed <- scope.closeIncomingWarmOperationV1() }()
	<-canceled
	select {
	case <-closed:
		t.Fatal("incoming operation cleanup returned before its worker joined")
	default:
	}
	close(done)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !scope.released {
		t.Fatal("joined original warm lane remained reusable")
	}
	if err := scope.closeIncomingWarmOperationV1(); err != nil {
		t.Fatal(err)
	}
}

func TestUncertainWarmMutationRetainsItsOriginalBorrow(t *testing.T) {
	failure := errors.New("actual member cleanup did not acknowledge")
	operation := &host.SessionOperation{}
	done := make(chan struct{})
	close(done)
	scope := &nativeStopScopeV1{operation: operation, warmDone: done, warmUncertain: failure, warmMutation: true}
	if err := scope.closeIncomingWarmOperationV1(); !errors.Is(err, failure) {
		t.Fatal("uncertain mutation was acknowledged as cleaned", err)
	}
	if scope.operation != operation || scope.released {
		t.Fatal("uncertain mutation released its original borrowed operation")
	}
}
