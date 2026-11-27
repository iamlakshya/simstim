package egress

import (
	"bytes"
	"encoding/json"
	"io"
)

// Match encoding/json's existing nesting ceiling. Tokens are inspected only
// for duplicate object keys, before one strict version-selected typed decode.
const maxControlJSONDepth = 10000

type controlJSONScope struct {
	object bool
	key    bool
	seen   map[string]bool
}

func uniqueControlJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var stack []controlJSONScope
	roots := 0
	finish := func() {
		if len(stack) == 0 {
			roots++
		} else if stack[len(stack)-1].object {
			stack[len(stack)-1].key = true
		}
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if roots == 1 && len(stack) == 0 {
				return nil
			}
			return registryControlError()
		}
		if err != nil {
			return registryControlError()
		}
		if delimiter, compound := token.(json.Delim); compound {
			switch delimiter {
			case '{', '[':
				if len(stack) == maxControlJSONDepth {
					return registryControlError()
				}
				stack = append(stack, controlJSONScope{object: delimiter == '{', key: delimiter == '{', seen: make(map[string]bool)})
			case '}', ']':
				if len(stack) == 0 {
					return registryControlError()
				}
				stack = stack[:len(stack)-1]
				finish()
			}
			continue
		}
		if len(stack) != 0 && stack[len(stack)-1].object && stack[len(stack)-1].key {
			key, stringKey := token.(string)
			parent := &stack[len(stack)-1]
			if !stringKey || parent.seen[key] {
				return registryControlError()
			}
			parent.seen[key], parent.key = true, false
		} else {
			finish()
		}
	}
}

func registryControlError() error {
	return memberError("bad_request", "invalid, duplicate or unsupported egress control arguments", "Update both helpers and retry the exact approved operation.")
}
