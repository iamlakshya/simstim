package proto

const CtlRefsV1 = "refs"

type CtlRefsArgsV1 struct {
	Pulled []string `json:"pulled,omitempty"`
}

// CtlRefsResultV1 is a guest claim. It supplies no host git or disk authority.
type CtlRefsResultV1 struct {
	Refs  []BundleRef `json:"refs"`
	Dirty bool        `json:"dirty"`
}
