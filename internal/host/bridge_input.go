package host

import "io"

// OwnedBridgeInput transfers cancellation and join rights for the sole bridge
// reader. Cancel interrupts Read; Wait joins its actual owned reader resource.
// A borrowed file whose Close may not interrupt a read does not meet this API.
type OwnedBridgeInput interface {
	io.Reader
	Cancel() error
	Wait() error
}
