package files

// Mount is one granted folder. The supervisor opens Host without following links.
type Mount struct {
	Host  string
	Guest string
	Write bool
	// Identity pins a resolved explicit grant to its original source object.
	Identity string `json:"identity,omitempty"`
}
