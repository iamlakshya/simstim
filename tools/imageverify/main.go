// The image verifier checks one Sigstore bundle without network access.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/imageverify"
)

// Version is set by the release build and must match its manifest entry.
var Version = "dev"

func main() {
	if len(os.Args) != 2 || os.Args[1] != "--image-signature-v1" {
		fail(fmt.Errorf("use --image-signature-v1 with one bounded request on stdin"))
	}
	if err := run(os.Stdin, os.Stdout); err != nil {
		fail(err)
	}
}

func run(input io.Reader, output io.Writer) error {
	request, data, err := imageverify.ReadRequest(input)
	if err != nil {
		return err
	}
	if err := verifyImageSignature(context.Background(), request.Bundle, request.Payload, request.Digest, request.Identity, request.Issuer, request.TrustedRoot); err != nil {
		return err
	}
	response := imageverify.ResponseV1{Version: imageverify.Version, Role: imageverify.Role, HelperVersion: Version, RequestSHA256: imageverify.RequestDigest(data)}
	return json.NewEncoder(output).Encode(response)
}

func fail(err error) {
	message := err.Error()
	if len(message) > imageverify.MaxDiagnostic-64 {
		message = message[:imageverify.MaxDiagnostic-64]
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n\nTry: check the image policy and signature.\n", message)
	os.Exit(1)
}
