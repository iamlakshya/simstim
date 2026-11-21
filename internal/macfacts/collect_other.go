//go:build !darwin

package macfacts

import "context"

// Read has no native Mac source on another platform. It claims no known fact.
func Read(context.Context) Facts { return Facts{Version: Version, Kind: "mac"} }
