//go:build http2legacy

package egress

// x/net v0.62.0 uses this same global tag to select its original server.
const physicalH2LegacyServerCompiled = true
