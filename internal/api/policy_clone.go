package api

import "reflect"

// ClonePolicy copies all mutable policy and source data with the same deep
// clone kernel as CloneLayers and CloneLayerValues. Nil and present-empty
// collections, method pointers, complete instants and source pins survive.
func ClonePolicy(policy Policy) Policy {
	return cloneLayerData(reflect.ValueOf(policy)).Interface().(Policy)
}
