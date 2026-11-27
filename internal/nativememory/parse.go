// Package nativememory reads current native memory accounting without turning
// unknown reporting values into capacity or claiming an allocation reservation.
package nativememory

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrUnknown marks unavailable or unprovable native accounting.
var ErrUnknown = errors.New("native memory accounting is unknown")

// ErrUnsupported marks a native accounting mode this build cannot prove.
var ErrUnsupported = errors.New("native memory accounting is unsupported")

// LimitKind distinguishes unknown, finite zero and explicitly unlimited.
type LimitKind uint8

const (
	// Unknown grants no known numeric value or unlimited interpretation.
	Unknown LimitKind = iota
	// Finite includes an explicitly known zero-byte limit.
	Finite
	// Unlimited requires a known kernel unlimited role or token.
	Unlimited
)

// Limit holds one parsed kernel limit. Its zero value is unknown.
type Limit struct {
	kind  LimitKind
	bytes uint64
}

// Kind returns the explicit state of the limit.
func (l Limit) Kind() LimitKind { return l.kind }

// Bytes returns a finite value, including zero. Unlimited is not numeric zero.
func (l Limit) Bytes() (uint64, bool) { return l.bytes, l.kind == Finite }

type contextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.in.Read(p)
	if stopped := r.ctx.Err(); stopped != nil {
		return n, stopped
	}
	return n, err
}

func decimal(text string) (uint64, error) {
	if text == "" {
		return 0, ErrUnknown
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return 0, ErrUnknown
		}
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, errors.Join(ErrUnknown, err)
	}
	return n, nil
}

func scalarToken(ctx context.Context, input io.Reader) (string, error) {
	if ctx == nil || input == nil {
		return "", ErrUnknown
	}
	scanner := bufio.NewScanner(contextReader{ctx: ctx, in: input})
	scanner.Split(bufio.ScanWords)
	if !scanner.Scan() {
		return "", errors.Join(ErrUnknown, scanner.Err(), ctx.Err())
	}
	text := scanner.Text()
	if scanner.Scan() {
		return "", fmt.Errorf("read native memory scalar: %w", ErrUnknown)
	}
	if err := errors.Join(scanner.Err(), ctx.Err()); err != nil {
		return "", errors.Join(ErrUnknown, err)
	}
	return text, nil
}

// ReadScalar reads one unsigned kernel decimal and preserves every read error.
func ReadScalar(ctx context.Context, input io.Reader) (uint64, error) {
	text, err := scalarToken(ctx, input)
	if err != nil {
		return 0, err
	}
	return decimal(text)
}

// ReadLimit recognises only the explicit kernel max token as unlimited.
func ReadLimit(ctx context.Context, input io.Reader) (Limit, error) {
	text, err := scalarToken(ctx, input)
	if err != nil {
		return Limit{}, err
	}
	if text == "max" {
		return Limit{kind: Unlimited}, nil
	}
	n, err := decimal(text)
	if err != nil {
		return Limit{}, err
	}
	return Limit{kind: Finite, bytes: n}, nil
}

// MeminfoKey is one fixed native byte-valued meminfo role.
type MeminfoKey string

const (
	// MemTotal names physical RAM reported by this kernel.
	MemTotal MeminfoKey = "MemTotal"
	// MemAvailable names this kernel's available-without-swap estimate.
	MemAvailable MeminfoKey = "MemAvailable"
	// SwapTotal is reporting data and never extends admission headroom.
	SwapTotal MeminfoKey = "SwapTotal"
)

// Meminfo retains known selected fields and their common read outcome.
type Meminfo struct {
	values map[MeminfoKey]uint64
	err    error
}

// Value returns a known field. Missing and failed reads never become zero.
func (m Meminfo) Value(key MeminfoKey) (uint64, error) {
	if m.err != nil {
		return 0, m.err
	}
	n, found := m.values[key]
	if !found {
		return 0, ErrUnknown
	}
	return n, nil
}

// ReadMeminfo is the single selected-field lexer for reporting and admission.
// Native kB conversion is checked; unknown units and duplicate fields refuse.
func ReadMeminfo(ctx context.Context, input io.Reader, keys ...MeminfoKey) (out Meminfo, failure error) {
	defer func() { out.err = failure }()
	if ctx == nil || input == nil || len(keys) == 0 || len(keys) > 3 {
		return out, ErrUnknown
	}
	out.values = make(map[MeminfoKey]uint64, len(keys))
	wanted := make(map[string]MeminfoKey, len(keys))
	for _, key := range keys {
		if key != MemTotal && key != MemAvailable && key != SwapTotal {
			return out, ErrUnknown
		}
		wanted[string(key)] = key
	}
	scanner := bufio.NewScanner(contextReader{ctx: ctx, in: input})
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), ":")
		key, requested := wanted[name]
		if !found || !requested {
			continue
		}
		if _, duplicate := out.values[key]; duplicate {
			return out, fmt.Errorf("read duplicate native %s: %w", name, ErrUnknown)
		}
		fields := strings.Fields(value)
		if len(fields) != 2 || fields[1] != "kB" {
			return out, fmt.Errorf("read native %s units: %w", name, ErrUnknown)
		}
		n, err := decimal(fields[0])
		if err != nil || n > ^uint64(0)/1024 {
			return out, errors.Join(ErrUnknown, err)
		}
		out.values[key] = n * 1024
	}
	if err := errors.Join(scanner.Err(), ctx.Err()); err != nil {
		return out, errors.Join(ErrUnknown, err)
	}
	for _, key := range keys {
		if _, found := out.values[key]; !found {
			return out, fmt.Errorf("read missing native %s: %w", key, ErrUnknown)
		}
	}
	return out, nil
}
