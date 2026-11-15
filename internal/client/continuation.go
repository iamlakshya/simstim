package client

import "context"

type inputContinuationDialKey struct{}

// withInputContinuationDial transfers only reader ownership. The actual H5
// detached original-Source transport expectation remains mandatory separately.
func withInputContinuationDial(ctx context.Context) context.Context { return context.WithValue(ctx,inputContinuationDialKey{},true) }
func inputContinuationDial(ctx context.Context) bool {value,_:=ctx.Value(inputContinuationDialKey{}).(bool);return value}
func exclusiveContinuationDial(ctx context.Context) bool {return execDial(ctx)||eventsDial(ctx)||inputContinuationDial(ctx)||reverseDial(ctx)}
