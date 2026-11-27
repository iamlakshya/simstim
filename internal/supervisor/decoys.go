package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/proto"
)

func newDecoys(ctx context.Context, enabled bool) (*proto.DecoyFilesV1, []api.DecoyTripwire, error) {
	return makeDecoys(ctx, enabled, rand.Reader)
}

// The entropy dependency permits deterministic failure/cancellation tests.
// Production calls only newDecoys, with the operating system's crypto/rand.
func makeDecoys(ctx context.Context, enabled bool, entropy io.Reader) (*proto.DecoyFilesV1, []api.DecoyTripwire, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !enabled {
		return nil, nil, nil
	}
	if entropy == nil {
		return nil, nil, wallError("cannot generate decoy credentials", io.ErrUnexpectedEOF)
	}
	definitions, err := guest.DecoyDefinitions()
	if err != nil {
		return nil, nil, err
	}
	files := &proto.DecoyFilesV1{Version: 1}
	tripwires := make([]api.DecoyTripwire, 0, len(definitions))
	seen := map[api.Secret]bool{}
	for _, definition := range definitions {
		tripwire := api.DecoyTripwire{Name: definition.Name, Hosts: append([]string(nil), definition.Hosts...)}
		for _, format := range definition.Values {
			value, err := makeDecoyValue(ctx, format, entropy)
			if err != nil {
				return nil, nil, err
			}
			if seen[value] {
				return nil, nil, wallError("decoy credentials are not unique", io.ErrUnexpectedEOF)
			}
			seen[value] = true
			tripwire.Values = append(tripwire.Values, value)
		}
		content, err := guest.RenderDecoy(definition.Name, tripwire.Values)
		if err != nil {
			return nil, nil, err
		}
		files.Files = append(files.Files, proto.DecoyFileV1{Name: definition.Name, Content: string(content)})
		tripwires = append(tripwires, tripwire)
	}
	encoded, err := json.Marshal(files)
	if err != nil || len(encoded) > guest.DecoyPayloadMax {
		return nil, nil, wallError("decoy credentials exceed the private start bound", err)
	}
	if err := guest.ValidateDecoyFilesV1(files); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return files, tripwires, nil
}

func makeDecoyValue(ctx context.Context, format guest.DecoyValueFormat, entropy io.Reader) (api.Secret, error) {
	var result strings.Builder
	result.WriteString(format.Prefix)
	alphabet := len(format.Alphabet)
	if alphabet < 2 || alphabet > 128 || format.Length <= len(format.Prefix) || format.Length > 128 {
		return "", wallError("the decoy credential format is invalid", nil)
	}
	// Rejection sampling is uniform even when the alphabet is not a power
	// of two. The fixed draw budget bounds a faulty entropy source as well.
	limit := 256 - 256%alphabet
	for draws := 0; result.Len() < format.Length && draws < 4096; draws++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var sample [1]byte
		if _, err := io.ReadFull(entropy, sample[:]); err != nil {
			return "", wallError("cannot generate decoy credentials", err)
		}
		if int(sample[0]) < limit {
			result.WriteByte(format.Alphabet[int(sample[0])%alphabet])
		}
	}
	if result.Len() != format.Length {
		return "", wallError("decoy randomness exhausted its draw budget", nil)
	}
	return api.Secret(result.String()), ctx.Err()
}
