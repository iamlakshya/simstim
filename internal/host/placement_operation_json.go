package host

import "encoding/json"

// The private raw encoding keeps operation receipts comparable while retaining
// future fields. Such receipts remain stored bytes and grant no authority.
func (record *placementOperationRecord) UnmarshalJSON(data []byte) error {
	type plain placementOperationRecord
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"pid", "birth", "kind", "at"})
	if err != nil {
		return err
	}
	*record = placementOperationRecord(value)
	if len(extra) != 0 {
		raw, err := json.Marshal(extra)
		if err != nil {
			return err
		}
		record.extra = string(raw)
	}
	return nil
}

func (record placementOperationRecord) MarshalJSON() ([]byte, error) {
	type plain placementOperationRecord
	var extra map[string]json.RawMessage
	if record.extra != "" {
		if err := json.Unmarshal([]byte(record.extra), &extra); err != nil {
			return nil, placementError("the operation receipt has unreadable future fields")
		}
	}
	return encodeFootprint(plain(record), extra)
}
