package wall

import "context"

// GuestBootCertifierV1 is the optional in-process VM compatibility consumer.
// The canonical supervisor calls it only after its actual VM prepare and
// primary PTY start responses. ABI strings supply no pin or execution authority.
// A headless preparation and an ordinary link hello cannot certify this boot.
type GuestBootCertifierV1 interface {
	CertifyGuestBootV1(ctx context.Context, preparedABI, terminalABI string) error
}
