package provider

import "errors"

// SourceInspection contains current complete sharing evidence, not permission.
// Paths only add Git source refusals. The composition owner pins their actual
// native identities and keeps this inspection until native work has joined.
type SourceInspection struct {
	Stamp  string
	Paths  []string
	Config *SourceConfigPin
}

// Close releases only this operation's held read-only configuration witness.
func (s *SourceInspection) Close() error {
	if s == nil {
		return nil
	}
	return errors.Join(s.Config.Close())
}
