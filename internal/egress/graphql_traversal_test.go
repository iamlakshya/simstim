package egress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

func gqlLongShallowFragmentSource(depth int) string {
	var source strings.Builder
	fmt.Fprintf(&source, `mutation M($name:String!) { ...F%d } fragment F0 on Mutation { createRef(input:{repositoryId:"repo",name:$name,oid:"authored"}){clientMutationId} }`, depth)
	for i := 1; i <= depth; i++ {
		fmt.Fprintf(&source, ` fragment F%d on Mutation { ...F%d ...F%d }`, i, i-1, i-1)
	}
	return source.String()
}

func TestGraphQLLongShallowFragmentDAGUsesMemoisedExplicitStack(t *testing.T) {
	// Every source selection set is shallow. Fragment dependency edges are
	// not syntactic nesting and must not inherit a new expansion-depth cap.
	source := gqlLongShallowFragmentSource(gqlDepth + 1)
	for _, outside := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		prefix := brand.Name + "/model234567/"
		name := "refs/heads/" + prefix + "fix"
		if outside {
			name = "refs/heads/main"
		}
		need, err := inspectGraphQLRefWrite(ctx, Request{Host: "api.github.com", Port: 443, Method: "POST", Path: "/graphql"}, gqlTestBody(t, source, map[string]any{"name": name}, "M"), prefix)
		cancel()
		if err != nil || need != outside {
			t.Fatal("shallow valid fragment DAG acquired an expansion limit or changed selected-request authority", err)
		}
	}
	cycle := strings.Replace(source, `createRef(input:{repositoryId:"repo",name:$name,oid:"authored"}){clientMutationId}`, fmt.Sprintf("...F%d", gqlDepth+1), 1)
	if _, err := gqlTestInspect(t, cycle, map[string]any{"name": "refs/heads/main"}, "M"); err == nil {
		t.Fatal("long fragment cycle was accepted")
	}
}

type gqlTraversalCancelContext struct {
	context.Context
	cancel       context.CancelFunc
	calls, after int
}

func (c *gqlTraversalCancelContext) Err() error {
	c.calls++
	if c.calls == c.after {
		c.cancel()
	}
	return c.Context.Err()
}

func TestGraphQLExplicitTraversalObservesLiveCancellation(t *testing.T) {
	document, err := parseGQL(t.Context(), gqlLongShallowFragmentSource(gqlDepth+1))
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &gqlTraversalCancelContext{Context: base, cancel: cancel, after: 256}
	_, err = inspectGraphQLSelections(ctx, document, &document.operations[0], map[string]gqlValue{"name": {kind: 'S', text: "refs/heads/main"}}, brand.Name+"/model234567/")
	if !errors.Is(err, context.Canceled) || ctx.calls < ctx.after {
		t.Fatal("actual fragment traversal ignored a live cancelled context")
	}
	select {
	case <-base.Done():
	default:
		t.Fatal("cancellation fixture did not cancel its actual context")
	}
}
