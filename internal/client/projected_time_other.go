//go:build !go1.26 || go1.27

package client

// Other minor toolchains retain native parsing. Unknown patch releases of
// Go1.26 take this same fallback in projected_time_go126.go.
func parseProjectedTime(raw []byte) (projectedTime, error) {
	return parseProjectedTimeNative(raw)
}
