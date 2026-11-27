package vmneed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/iamlakshya/simstim/internal/image"
)

// MaxImageV1 bounds the closed image projection, independent of the generic
// frame cap. It contains only immutable pins, never credentials or paths.
const MaxImageV1 = 32 << 10

// EncodeImageV1 projects an already-selected original Need image. Encoding
// supplies data only; original creation history and native storage stay with
// their existing owners. A transfer token is not an immutable image identity.
func EncodeImageV1(request image.Request) ([]byte, error) {
	if err := validateImageV1(request); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxImageV1 {
		return nil, fmt.Errorf("the original image projection exceeds its bound")
	}
	return raw, nil
}

// DecodeImageV1 uses the SAME canonical nested field validator as VM Need.
// Case aliases, duplicates, unknown fields and trailing content are refused.
func DecodeImageV1(raw []byte) (image.Request, error) {
	var result image.Request
	if len(raw) == 0 || len(raw) > MaxImageV1 {
		return result, fmt.Errorf("the original image projection has invalid bounds")
	}
	canonical := json.NewDecoder(bytes.NewReader(raw))
	canonical.UseNumber()
	if err := canonicalValue(canonical, reflect.TypeFor[image.Request]()); err != nil {
		return result, err
	}
	var trailing any
	if err := canonical.Decode(&trailing); err != io.EOF {
		return result, fmt.Errorf("the original image projection has trailing content")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return image.Request{}, err
	}
	if err := validateImageV1(result); err != nil {
		return image.Request{}, err
	}
	return result.Clone(), nil
}
func validateImageV1(request image.Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Transfer != "" || request.RootHash == "" || request.DataBlocks <= 0 {
		return fmt.Errorf("the original image projection needs immutable complete root pins")
	}
	return nil
}
