package agents

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// The reasons that CheckID gives for an ID that it refuses.
var (
	// ErrNoIDForm is for an agent whose row has no IDPattern.
	ErrNoIDForm = errors.New("the agent table gives no form for this agent's session IDs")
	// ErrWrongIDForm is for an ID that does not match the agent's IDPattern.
	ErrWrongIDForm = errors.New("it does not have the form of the agent's session IDs")
)

// idForms holds, for each IDPattern, a function that compiles it the
// first time it runs, and then returns the same result.
var idForms sync.Map // string -> func() (*regexp.Regexp, error)

// compileIDForm compiles an IDPattern to match a whole ID. A test counts
// its calls.
var compileIDForm = func(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		return nil, fmt.Errorf("the agent table's form for this agent's session IDs does not compile: %w", err)
	}
	return re, nil
}

// idForm returns pattern compiled. Callers that ask at the same time, the
// first time, share one compile: the function is stored before it runs.
func idForm(pattern string) (*regexp.Regexp, error) {
	v, ok := idForms.Load(pattern)
	if !ok {
		v, _ = idForms.LoadOrStore(pattern, sync.OnceValues(func() (*regexp.Regexp, error) { return compileIDForm(pattern) }))
	}
	compile, _ := v.(func() (*regexp.Regexp, error))
	return compile()
}

// CheckID reports whether id, as a whole, has the form of the agent's own
// session IDs, IDPattern. It returns nil when it does. An agent without a
// pattern has no known form, so no ID passes (ErrNoIDForm). A pattern that
// does not compile passes no ID either, and the error says so.
func (a Agent) CheckID(id string) error {
	if a.IDPattern == "" {
		return ErrNoIDForm
	}
	re, err := idForm(a.IDPattern)
	switch {
	case err != nil:
		return err
	case !re.MatchString(id):
		return ErrWrongIDForm
	}
	return nil
}

// ValidID reports whether id has the form of the agent's own session IDs
// (CheckID).
func (a Agent) ValidID(id string) bool { return a.CheckID(id) == nil }

// ResumeTemplate returns the resume command to expand for a session that
// saved stored, a copy of Resume from when it started. An older helper
// saved the same command without its "--". That one gets the current
// command, so that the ID stays a positional argument. Any other command
// stays as it was saved.
func (a Agent) ResumeTemplate(stored []string) []string {
	older := slices.DeleteFunc(slices.Clone(a.Resume), func(s string) bool { return s == "--" })
	if len(older) < len(a.Resume) && slices.Equal(stored, older) {
		return slices.Clone(a.Resume)
	}
	return stored
}

// Expand replaces placeholders in argv.
func Expand(argv []string, vars map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}
		out[i] = a
	}
	return out
}
