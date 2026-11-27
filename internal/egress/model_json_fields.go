package egress

import (
	"encoding/json"
	"errors"
	"strings"
)

func (scan *modelJSONScan) tools(depth int) (modelJSONValue, error) {
	token, err := scan.decoder.Token()
	if err != nil {
		return modelJSONValue{}, err
	}
	if token == nil {
		return modelJSONValue{}, nil
	}
	if token != json.Delim('[') {
		return modelJSONValue{}, errors.New("model tools must be an array of objects")
	}
	result := modelJSONValue{kind: '['}
	for scan.decoder.More() {
		child, err := scan.value(false, true, depth+1)
		if err != nil {
			return modelJSONValue{}, err
		}
		if child.kind != '{' {
			return modelJSONValue{}, errors.New("model tool entry must be an object")
		}
		result.nonempty = true
	}
	_, err = scan.decoder.Token()
	return result, err
}

func modelUnsupportedURI(value string) bool {
	value = strings.TrimSpace(value)
	scheme, _, found := strings.Cut(value, ":")
	if found {
		return !strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https") && !strings.EqualFold(scheme, "data")
	}
	// Dedicated file IDs and bare opaque IDs are not provider fetches. A
	// relative/network path is not a file ID and cannot silently gain scope.
	return value == "" || strings.ContainsAny(value, "/\\")
}

func (s *Server) modelAuditRule(model *modelRequest, feature string) string {
	rule := "model:" + feature
	if display := model.display[feature]; display != "" && display != feature {
		rule += "/" + cleanAuditText(display, s.scrubber.snapshot(), 96)
	}
	return rule
}
