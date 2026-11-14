package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var errMembershipGenerationMissing = errors.New("membership marker absent or malformed")

const membershipGenerationPrefix = "# membership-generation: "

func newMembershipGeneration() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// Read only the exact canonical first line. A token inside a TOML value,
// later comment or legacy header is never membership authority.
func readMembershipGeneration(ctx context.Context, source io.ReaderAt) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var line [len(membershipGenerationPrefix) + 32 + 1]byte
	n, err := source.ReadAt(line[:], 0)
	if stopped := ctx.Err(); stopped != nil {
		return "", stopped
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if n != len(line) || string(line[:len(membershipGenerationPrefix)]) != membershipGenerationPrefix || line[len(line)-1] != '\n' {
		return "", fmt.Errorf("%w: %w", hostStateRemovalError("the current host membership has no authoritative generation marker"), errMembershipGenerationMissing)
	}
	generation := string(line[len(membershipGenerationPrefix) : len(line)-1])
	if !ValidHostID(generation) {
		return "", fmt.Errorf("%w: %w", hostStateRemovalError("the current host membership generation is malformed"), errMembershipGenerationMissing)
	}
	return generation, nil
}
