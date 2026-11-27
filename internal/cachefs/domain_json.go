package cachefs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

var errDomainObject = errors.New("invalid native domain object")

// The storage layer parses only its two closed domain schemas. It must not
// import the application API, which also uses signed runtime storage.
// Values remain data; the original native sentinel supplies ownership.
func domainObjectFields(ctx context.Context, raw []byte, names ...string) (map[string]json.RawMessage, error) {
	if ctx == nil {
		return nil, errDomainObject
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(domainReader{ctx: ctx, reader: bytes.NewReader(raw)})
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errDomainObject
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] {
			return nil, errDomainObject
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, errDomainObject
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errDomainObject
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != len(allowed) {
		return nil, errDomainObject
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errDomainObject
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return fields, nil
}
