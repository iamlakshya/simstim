package client

import "github.com/iamlakshya/simstim/internal/api"

// A private method view lets the real bounded object preflight and ordinary
// raw readers share ONE set of codec predicates and ONE underlying parser.
// It grants no source, state support, receipt, fetch or outbound authority.
type jsonValueProjection interface {
	ObjectRoot() (api.JSONSpan, error)
	Object(api.JSONSpan, []string, func(api.JSONField) error) error
	Array(api.JSONSpan, func(api.JSONSpan) error) error
	Literal(api.JSONSpan, string) (bool, error)
	StringEqual(api.JSONSpan, string) (bool, error)
	CompareStrings(api.JSONSpan, api.JSONSpan) (int, error)
	WalkString(api.JSONSpan, func(rune) error) error
	WalkEncoded(api.JSONSpan, func([]byte) error) error
	OrderedStrings(api.JSONSpan) (api.OrderedJSONStrings, error)
	OrderedFields(api.JSONSpan) (api.OrderedJSONFields, error)
	OrderedStringArraySubset(api.JSONSpan, api.JSONSpan) (bool, error)
	Fields(api.JSONSpan) (api.JSONFields, error)
	Values(api.JSONSpan) (api.JSONValues, error)
}

var _ jsonValueProjection = (*api.JSONProjection)(nil)
