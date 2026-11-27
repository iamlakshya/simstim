//go:build !linux

package slotlaunch

// CapacityNativeV1 is unknown where there is no native Linux slot producer.
func CapacityNativeV1() (CapacityProjectionV1, bool) { return CapacityProjectionV1{}, false }
