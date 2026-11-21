// Package classify contains the optional judge's bounded data protocol.
// The egress owner runs the fixed checks and supplies trusted session facts.
package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	// JudgeVersion identifies the sole structured input contract.
	JudgeVersion = 1
	// MaxInputBytes bounds one complete judge request before API wrapping.
	MaxInputBytes = 16 << 10
	// MaxAnswerBytes bounds a command's complete JSON answer.
	MaxAnswerBytes = 4 << 10
)

// FactStatus distinguishes disabled and failed lookups from measured facts.
type FactStatus string

const (
	FactDisabled FactStatus = "disabled"
	FactUnknown  FactStatus = "unknown"
	FactKnown    FactStatus = "known"
)

// KnownBool records whether the trusted owner knows the observation.
type KnownBool struct {
	Known bool `json:"known"`
	Value bool `json:"value"`
}

// AgeFact carries an age only when the lookup succeeded.
type AgeFact struct {
	Status FactStatus `json:"status"`
	Days   uint32     `json:"days,omitempty"`
}

// RankFact carries a positive rank only when a configured list contained it.
type RankFact struct {
	Status FactStatus `json:"status"`
	Rank   uint64     `json:"rank,omitempty"`
}

// Facts are tags, never permission to reach a destination.
type Facts struct {
	PreviouslyReached KnownBool `json:"previously_reached"`
	DomainAge         AgeFact   `json:"domain_age"`
	Popularity        RankFact  `json:"popularity"`
}

// Input contains only the fields of the classifier's judge protocol. The
// owner source-checks every field before calling a foreign model. Host,
// Method and Path are guest-chosen data. No body, query or explanation fits.
type Input struct {
	Version           int      `json:"version"`
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	RegistrableDomain string   `json:"registrable_domain"`
	Facts             Facts    `json:"facts"`
	Method            string   `json:"method,omitempty"`
	Path              string   `json:"path,omitempty"`
	RepositoryRemote  string   `json:"repository_remote,omitempty"`
	Ecosystems        []string `json:"ecosystems,omitempty"`
	Agent             string   `json:"agent,omitempty"`
	Profile           string   `json:"profile,omitempty"`
	Task              string   `json:"task,omitempty"`
}

// Result is advisory. Only the egress owner's current policy transaction
// can publish a rule, and it source-checks Reason before public output.
type Result struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Scope  string `json:"scope"`
}

// Judge cannot choose a destination, obtain a key or publish a rule.
type Judge interface {
	Decide(context.Context, Input) (Result, error)
}

var (
	errInput  = errors.New("judge input is invalid or too large. Try: check trusted session metadata and the intercepted destination")
	errAnswer = errors.New("judge answer is invalid or too large. Try: return one JSON action, reason and once or session scope")
)

// EncodeInput owns and validates the fixed shape, and cuts Path at a UTF-8
// boundary within 256 bytes. The caller alone knows whether interception
// permits Method and Path, and omits both for opaque traffic.
func EncodeInput(input Input) ([]byte, error) {
	if input.Version != JudgeVersion || !domainData(input.Host) || input.Port < 1 || input.Port > 65535 || input.RegistrableDomain != "" && !domainData(input.RegistrableDomain) || !validFacts(input.Facts) {
		return nil, errInput
	}
	if input.Method != "" && !api.ValidHTTPMethod(input.Method) || input.Method == "" && input.Path != "" || strings.ContainsAny(input.Path, "?#") || input.Path != "" && !strings.HasPrefix(input.Path, "/") {
		return nil, errInput
	}
	if !textData(input.Path, MaxInputBytes) || !textData(input.RepositoryRemote, 2048) || !textData(input.Agent, 64) || !textData(input.Profile, 128) || !textData(input.Task, 4096) || len(input.Ecosystems) > 16 {
		return nil, errInput
	}
	for _, ecosystem := range input.Ecosystems {
		if ecosystem == "" || !textData(ecosystem, 64) {
			return nil, errInput
		}
	}
	input.Path = cutBytes(input.Path, 256)
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > MaxInputBytes {
		return nil, errInput
	}
	return raw, nil
}

func domainData(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, c := range []byte(value) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func textData(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validFacts(facts Facts) bool {
	if !facts.PreviouslyReached.Known && facts.PreviouslyReached.Value {
		return false
	}
	validStatus := func(status FactStatus) bool {
		return status == FactDisabled || status == FactUnknown || status == FactKnown
	}
	return validStatus(facts.DomainAge.Status) && validStatus(facts.Popularity.Status) &&
		(facts.DomainAge.Status == FactKnown || facts.DomainAge.Days == 0) &&
		(facts.Popularity.Status == FactKnown && facts.Popularity.Rank > 0 || facts.Popularity.Status != FactKnown && facts.Popularity.Rank == 0)
}

func cutBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// DecodeResult refuses aliases, duplicate keys, extra fields and extra JSON
// values. A valid wider scope is cut to the owner's configured maximum.
func DecodeResult(raw []byte, maxScope string) (Result, error) {
	return decodeResult(nil, raw, maxScope)
}

func decodeResult(ctx context.Context, raw []byte, maxScope string) (Result, error) {
	if len(raw) == 0 || len(raw) > MaxAnswerBytes || !utf8.Valid(raw) || !api.ExactJSONFields(raw, Result{}) {
		return Result{}, errAnswer
	}
	fields, err := api.JSONObjectFields(raw, "action", "reason", "scope")
	if err != nil || len(fields) != 3 {
		return Result{}, errAnswer
	}
	for _, field := range []string{"action", "reason", "scope"} {
		if len(fields[field]) == 0 || bytes.Equal(bytes.TrimSpace(fields[field]), []byte("null")) {
			return Result{}, errAnswer
		}
	}
	var result Result
	if json.Unmarshal(raw, &result) != nil {
		return Result{}, errAnswer
	}
	if reasonSanitizerOf(ctx) != nil {
		return SanitizeResult(ctx, result, maxScope)
	}
	return BoundResult(result, maxScope)
}

// BoundResult also validates answers from in-process test judges.
func BoundResult(result Result, maxScope string) (Result, error) {
	if result.Action != "allow" && result.Action != "deny" && result.Action != "unsure" || result.Scope != "once" && result.Scope != "session" || maxScope != "once" && maxScope != "session" || !utf8.ValidString(result.Reason) {
		return Result{}, errAnswer
	}
	result.Reason = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return -1
		}
		return c
	}, result.Reason)
	if utf8.RuneCountInString(result.Reason) > 200 {
		result.Reason = string([]rune(result.Reason)[:200])
	}
	if maxScope == "once" {
		result.Scope = "once"
	}
	return result, nil
}
