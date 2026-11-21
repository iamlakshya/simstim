package client

import (
	"bytes"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
)

// A generic Call DTO or a nested member cannot acquire the primary's
// protected delivery. Only the exact original root argument constructor can
// reach its private plan comparison in the physical writer.
func checkTrustedJudgeWireFieldsV1(raw []byte, allowRoot bool) error {
	return checkTrustedJudgeWireRowV1(raw, allowRoot, false)
}

func checkTrustedJudgeWireRowV1(raw []byte, allowRoot, member bool) error {
	fields, err := api.JSONObjectFields(raw, "protected_trusted_secret_values", "trusted_uses", "secret_delivery", "members")
	if err != nil {
		return trustedJudgeSourceErrorV1()
	}
	if !allowRoot && (len(fields["protected_trusted_secret_values"]) != 0 || len(fields["trusted_uses"]) != 0) {
		return trustedJudgeSourceErrorV1()
	}
	if delivery := fields["secret_delivery"]; len(delivery) != 0 && !bytes.Equal(bytes.TrimSpace(delivery), []byte("null")) {
		intent, err := api.JSONObjectFields(delivery, "trusted_uses")
		if err != nil || !allowRoot && len(intent["trusted_uses"]) != 0 {
			return trustedJudgeSourceErrorV1()
		}
	}
	if members := fields["members"]; len(members) != 0 && !bytes.Equal(bytes.TrimSpace(members), []byte("null")) {
		if member {
			return trustedJudgeSourceErrorV1()
		}
		var rows []json.RawMessage
		if json.Unmarshal(members, &rows) != nil {
			return trustedJudgeSourceErrorV1()
		}
		for _, row := range rows {
			if err := checkTrustedJudgeWireRowV1(row, false, true); err != nil {
				return err
			}
		}
	}
	return nil
}
