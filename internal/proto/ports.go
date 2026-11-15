package proto

// Versioned control operations remain on the existing guest control stream.
const (
	CtlPortsV1        = "ports.list-v1"
	CtlReverseStartV1 = "port.reverse.start-v1"
	CtlReverseStopV1  = "port.reverse.stop-v1"
)

// PortHeaderV1 permits only one exact guest loopback TCP destination.
type PortHeaderV1 struct {
	Version int `json:"v"`
	Port    int `json:"port"`
}

// ReversePortV1 correlates a listener and every connection with one retained
// supervisor operation. The lease string itself creates no authority.
type ReversePortV1 struct {
	PortHeaderV1
	Lease string `json:"lease"`
}
