package wall

import "context"

// ResourceFactoryV1 is the version1 trusted supervisor resource join. Its
// implementation obtains a fresh ExpectedSpec from the admitted client Need
// pipeline. A saved session folder alone cannot authorise execution.
type ResourceFactoryV1 interface {
	Version() int
	// Build selects Prepare or Open from owned layout existence only.
	// Execution authority always comes from the separate trusted ExpectedSpec.
	Build(context.Context) (Sandbox, error)
	Prepare(context.Context) (Sandbox, error)
	Open(context.Context, string) (Sandbox, error)
}
