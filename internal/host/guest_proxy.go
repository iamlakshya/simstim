package host

// DefaultGuestProxy is the existing explicit proxy inside the guest network
// namespace. It never names a listening address on the trusted host.
const DefaultGuestProxy = "http://127.0.0.1:3128"
