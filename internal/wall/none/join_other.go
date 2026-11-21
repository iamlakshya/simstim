//go:build !linux

package none

import "context"

type JoinOwnerV1 struct{}

func (*LiveOwnerV1) PrepareJoinV1(context.Context) (*JoinOwnerV1, error) {
	return nil, failure("the native unwalled completion producer needs a supported Linux host", nil)
}
func (*JoinOwnerV1) CheckJoinedV1(context.Context) error {
	return failure("the original native unwalled completion producer is unavailable", nil)
}
func (*JoinOwnerV1) Close() error { return nil }
