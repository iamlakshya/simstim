//go:build linux

package host

import "testing"

func TestNoneUnresolvedStopCannotCloseItsOriginalOperation(t *testing.T) {
	operation := &SessionOperation{}
	operation.active.Store(true)
	source := &StoppedNoneSessionV1{operation: operation, committed: true}
	source.self = source
	operation.noneStop = source
	if err := source.Close(); err == nil {
		t.Fatal("an unresolved committed stop reported closure")
	}
	if err := operation.CloseWithError(); err == nil {
		t.Fatal("an unresolved stop released its SAME owning operation")
	}
	if !operation.active.Load() || operation.noneStop != source || source.closed {
		t.Fatal("an error discarded the original stop or deactivated its operation")
	}
}
