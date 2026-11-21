//go:build !http2legacy

package egress

// A Go1.27 build without the explicit legacy tag must not admit interception.
const physicalH2LegacyServerCompiled = false
