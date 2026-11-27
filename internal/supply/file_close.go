package supply

// FileCloseFactsV1 describes only one concrete descriptor-close operation.
// These facts grant no role, source, publication, native peer or OP authority.
// Each actual caller must freeze and join its own borrowers before calling.
type FileCloseFactsV1 struct {
	Retired    bool
	Diagnostic error
	Unknown    error
}
