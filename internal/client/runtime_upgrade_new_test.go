package client

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestNewVMRuntimeRejectsForeignOriginalRequestBeforeTransport(t *testing.T) {
	original := NewGrantRequest(false, false)
	foreign := NewGrantRequest(false, false)
	ctx := WithGrantRequest(context.Background(), original)
	conn := &Conn{}
	if _, err := conn.prepareVMNeedV1(ctx, VMNeedOptionsV1{}, foreign); err == nil {
		t.Fatal("a foreign request reached VM preparation")
	}
	if _, err := conn.prepareVMRuntimeV1(ctx, api.CapProbe{}, nil, foreign); err == nil {
		t.Fatal("a foreign request reached runtime delivery")
	}
}
