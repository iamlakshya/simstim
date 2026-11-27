package egress

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/nettarget"
)

// The authority goes through the existing judgeEndpoint/net/url/NormaliseHost
// kernel. Only a valid port's arbitrary leading zeroes may exceed this finite
// scratch: NormaliseHost already refuses non-ASCII or >253-byte DNS names.
// Unbounded custom base paths use CanonicalPath's SAME streaming grammar.
type trustedEndpointStreamV1 struct {
	ctx        context.Context
	prefix     [8]byte
	prefixUsed int
	host       [1024]byte
	hostUsed   int
	portStart  int
	portDigits bool
	port       int
	bracket    bool
	compressed bool
	path       *nettarget.PathValidatorV1
	tail       trustedEndpointTailV1
	authority  string
	fragment   bool
	err        error
}

type trustedEndpointTailV1 struct {
	data [17]byte // longest accepted canonical suffix: /chat/completions
	next int
	used int
}

func (v *trustedEndpointTailV1) Write(data []byte) (int, error) {
	for _, b := range data {
		v.data[v.next] = b
		v.next = (v.next + 1) % len(v.data)
		v.used = min(v.used+1, len(v.data))
	}
	return len(data), nil
}

func (v *trustedEndpointTailV1) suffix(want string) bool {
	if len(want) > v.used {
		return false
	}
	for i := range want {
		at := (v.next - len(want) + i + len(v.data)) % len(v.data)
		if v.data[at] != want[i] {
			return false
		}
	}
	return true
}

func newTrustedEndpointStreamV1(ctx context.Context) *trustedEndpointStreamV1 {
	return &trustedEndpointStreamV1{ctx: ctx, portStart: -1}
}

func (v *trustedEndpointStreamV1) Write(data []byte) (int, error) {
	for i, b := range data {
		if i&4095 == 0 {
			if err := v.ctx.Err(); err != nil {
				return i, err
			}
		}
		if err := v.byte(b); err != nil {
			v.err = err
			return i, err
		}
	}
	return len(data), nil
}

func (v *trustedEndpointStreamV1) WriteString(data string) (int, error) {
	for i := range len(data) {
		if i&4095 == 0 {
			if err := v.ctx.Err(); err != nil {
				return i, err
			}
		}
		if err := v.byte(data[i]); err != nil {
			v.err = err
			return i, err
		}
	}
	return len(data), nil
}

func (v *trustedEndpointStreamV1) byte(b byte) error {
	if v.err != nil {
		return v.err
	}
	if b < 0x20 || b == 0x7f || v.fragment {
		return registryIntentError()
	}
	if v.prefixUsed < len(v.prefix) {
		v.prefix[v.prefixUsed] = b
		v.prefixUsed++
		if v.prefixUsed == len(v.prefix) && !strings.EqualFold(string(v.prefix[:]), "https://") {
			return registryIntentError()
		}
		return nil
	}
	if v.path != nil {
		if b == '?' {
			return registryIntentError()
		}
		if b == '#' {
			v.fragment = true // net/url permits an empty final fragment only.
			return nil
		}
		v.prefix[0] = b // Reuse owned scratch; Write retains no input slice.
		_, err := v.path.Write(v.prefix[:1])
		return err
	}
	if b == '/' {
		if err := v.finishAuthority(); err != nil {
			return err
		}
		v.path = nettarget.NewPathValidatorV1(&v.tail)
		v.prefix[0] = b
		_, err := v.path.Write(v.prefix[:1])
		return err
	}
	if b >= 0x7f || b == '%' || b == '?' || b == '#' || b == '@' {
		return registryIntentError()
	}
	if v.compressed {
		if b < '0' || b > '9' {
			return registryIntentError() // It would make an overlong raw host.
		}
		v.port = v.port*10 + int(b-'0')
		if v.port > 65535 {
			return registryIntentError()
		}
		return nil
	}
	if v.hostUsed == len(v.host) {
		if v.portStart < 0 || !v.portDigits || v.bracket || b < '0' || b > '9' {
			return registryIntentError()
		}
		v.compressed = true
		return v.byte(b)
	}
	if v.hostUsed == 0 && b == '[' {
		v.bracket = true
	} else if b == ']' {
		v.bracket = false
	}
	v.host[v.hostUsed] = b
	v.hostUsed++
	if b == ':' && !v.bracket {
		v.portStart, v.portDigits, v.port = v.hostUsed, true, 0
	} else if v.portStart >= 0 {
		if b < '0' || b > '9' {
			v.portDigits = false
		} else if v.portDigits {
			v.port = v.port*10 + int(b-'0')
			if v.port > 65535 {
				return registryIntentError()
			}
		}
	}
	return nil
}

func (v *trustedEndpointStreamV1) finishAuthority() error {
	raw := string(v.host[:v.hostUsed])
	if v.compressed {
		raw = string(v.host[:v.portStart]) + strconv.Itoa(v.port)
	}
	endpoint, request, _, err := judgeEndpoint(string(v.prefix[:]) + raw + "/messages")
	if err != nil || endpoint.Scheme != "https" || request.Port != 443 {
		return registryIntentError()
	}
	v.authority = net.JoinHostPort(request.Host, "443")
	return nil
}

func (v *trustedEndpointStreamV1) Finish() (string, error) {
	if err := v.ctx.Err(); err != nil {
		return "", err
	}
	if v.err != nil {
		return "", v.err
	}
	if v.path == nil || v.path.Finish() != nil || !v.tail.suffix("/chat/completions") && !v.tail.suffix("/messages") {
		return "", registryIntentError()
	}
	return v.authority, nil
}
