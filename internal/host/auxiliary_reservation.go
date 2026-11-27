package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// AuxiliaryReservationV1 is protected accounting DATA for one original
// operation's native auxiliary boot. It creates no stopped, boot, unit,
// namespace, artifact, client receipt or deletion authority.
type AuxiliaryReservationV1 struct {
	Version  int       `json:"v"`
	Token    string    `json:"token"`
	BootID   string    `json:"boot_id"`
	Since    time.Time `json:"since"`
	Declared int64     `json:"declared"`
	opaque   json.RawMessage
}

func (AuxiliaryReservationV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, api.Redacted)
}
func auxiliaryReservationDataErrorV1() error {
	return UserError("auxiliary_reservation", "the protected auxiliary reservation is invalid or unsupported", "Keep its original operation and native resources; inspect them with the matching helper before retrying.")
}

// UnmarshalJSON uses the existing canonical object barrier. Future versions
// remain opaque for ordinary retention and fail every current auxiliary use.
func (reservation *AuxiliaryReservationV1) UnmarshalJSON(raw []byte) error {
	fields, err := api.JSONObjectFields(raw, "v", "token", "boot_id", "since", "declared")
	if err != nil {
		return auxiliaryReservationDataErrorV1()
	}
	var version int
	if len(fields["v"]) == 0 || json.Unmarshal(fields["v"], &version) != nil || version < 1 {
		return auxiliaryReservationDataErrorV1()
	}
	if version != 1 {
		*reservation = AuxiliaryReservationV1{Version: version, opaque: bytes.Clone(raw)}
		return nil
	}
	type wire AuxiliaryReservationV1
	if len(fields) != 5 || !api.ExactJSONFields(raw, wire{}) {
		return auxiliaryReservationDataErrorV1()
	}
	var data wire
	if json.Unmarshal(raw, &data) != nil {
		return auxiliaryReservationDataErrorV1()
	}
	*reservation = AuxiliaryReservationV1(data)
	return validateAuxiliaryReservationV1Data(reservation)
}
func (reservation AuxiliaryReservationV1) MarshalJSON() ([]byte, error) {
	if len(reservation.opaque) != 0 {
		return bytes.Clone(reservation.opaque), nil
	}
	if err := validateAuxiliaryReservationV1Data(&reservation); err != nil {
		return nil, err
	}
	type wire AuxiliaryReservationV1
	return json.Marshal(wire(reservation))
}
func cloneAuxiliaryReservationV1(input *AuxiliaryReservationV1) *AuxiliaryReservationV1 {
	if input == nil {
		return nil
	}
	copy := *input
	copy.opaque = bytes.Clone(input.opaque)
	return &copy
}

func isLowerHexV1(s string, wantLen int) bool {
	if len(s) != wantLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// This validates only representable accounting data. The actual producer
// separately supplies original OP, native absence, admission and lifetime.
func validateAuxiliaryReservationV1Data(reservation *AuxiliaryReservationV1) error {
	if reservation == nil {
		return nil
	}
	if reservation.Version != 1 || len(reservation.opaque) != 0 || !isLowerHexV1(reservation.Token, 32) || !auxiliaryBootIDV1(reservation.BootID) || reservation.Since.IsZero() || reservation.Since.Location() != time.UTC || reservation.Declared <= 0 {
		return auxiliaryReservationDataErrorV1()
	}
	return checkExactWalledCapV1(reservation.Declared)
}
func auxiliaryBootIDV1(boot string) bool {
	if len(boot) != 36 {
		return false
	}
	for index, value := range boot {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if value != '-' {
				return false
			}
			continue
		}
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}
