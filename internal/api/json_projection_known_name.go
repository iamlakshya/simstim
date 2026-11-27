package api

// KnownName exposes the same exact field-name and alias facts used by Object.
// name must be a key span from this projection's field cursor. The return is
// an index into the caller's finite DTO list, or -1 for an unknown field.
// It grants no source ownership, DTO support, receipt or name authority.
func (p *JSONProjection) KnownName(name JSONSpan, known []string) (int, error) {
	if p == nil {
		return -1, ErrJSONProjection
	}
	return p.knownName(name, known)
}
