package host

import "net"

// NativeStartupRoleV1 selects only the closed public code/namespace bootstrap.
// It is not execution authority; the genuine original OP loan is still required.
const NativeStartupRoleV1 = "native-startup-v1"

// NewNativeStartupLineV1 is the sole physical startup reader with at most
// three native roles. Typed claims, never JSON, choose hello versus OP roles.
func NewNativeStartupLineV1(connection net.Conn) (*StartupLineV1, error) {
	line, err := NewStartupLineV1(connection)
	if err != nil {
		return nil, err
	}
	line.loanFiles = 3
	return line, nil
}
