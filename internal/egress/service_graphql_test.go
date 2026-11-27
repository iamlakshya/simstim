package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func gqlTestBody(t *testing.T, query string, variables any, operation string) []byte {
	t.Helper()
	body := map[string]any{"query": query}
	if variables != nil {
		body["variables"] = variables
	}
	if operation != "" {
		body["operationName"] = operation
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
func gqlTestInspect(t *testing.T, query string, variables any, operation string) (bool, error) {
	t.Helper()
	return inspectGraphQLRefWrite(t.Context(), Request{Host: "api.github.com", Port: 443, Path: "/graphql", Method: "POST"}, gqlTestBody(t, query, variables, operation), brand.Name+"/model234567/")
}

func TestGraphQLAllSpecifiedRefMutationsUseObservableNamespaceOrEndpointAuthority(t *testing.T) {
	prefix := brand.Name + "/model234567/"
	for _, test := range []struct {
		mutation string
		need     bool
	}{
		{`createRef(input:{repositoryId:"repo",name:"refs/heads/` + prefix + `fix",oid:"authored"})`, false},
		{`createRef(input:{repositoryId:"repo",name:"refs/heads/main",oid:"authored"})`, true},
		{`createCommitOnBranch(input:{branch:{repositoryNameWithOwner:"org/repo",branchName:"` + prefix + `fix"},expectedHeadOid:"authored",message:{headline:"authored"}})`, false},
		{`createCommitOnBranch(input:{branch:{id:"opaque"},expectedHeadOid:"authored",message:{headline:"authored"}})`, true},
		{`updateRef(input:{refId:"opaque",oid:"authored",force:true})`, true},
		{`updateRefs(input:{repositoryId:"repo",refUpdates:[{name:"refs/heads/` + prefix + `fix",afterOid:"0000000000000000000000000000000000000000"}]})`, false},
		{`updateRefs(input:{repositoryId:"repo",refUpdates:{name:"refs/heads/main",afterOid:"authored"}})`, true},
		{`deleteRef(input:{refId:"opaque"})`, true},
		{`mergeBranch(input:{repositoryId:"repo",base:"` + prefix + `fix",head:"authored"})`, false},
		{`mergeBranch(input:{repositoryId:"repo",base:"main",head:"authored"})`, true},
		{`mergePullRequest(input:{pullRequestId:"opaque",mergeMethod:MERGE})`, true},
	} {
		t.Run(test.mutation, func(t *testing.T) {
			need, err := gqlTestInspect(t, `mutation { `+test.mutation+` { clientMutationId } }`, nil, "")
			if err != nil || need != test.need {
				t.Fatal("GraphQL ref mutation lost its exact namespace/opaque-ID contract", err)
			}
		})
	}
}

func TestGraphQLSelectedOperationVariablesDefaultsAliasesAndFragments(t *testing.T) {
	prefix := brand.Name + "/model234567/"
	query := `query Read { viewer { login } } mutation Write($input:CreateRefInput!) { alias:createRef(input:$input) { clientMutationId } }`
	if need, err := gqlTestInspect(t, query, nil, "Read"); err != nil || need {
		t.Fatal("unselected mutation changed the selected query authority", err)
	}
	variables := map[string]any{"input": map[string]any{"repositoryId": "repo", "name": "refs/heads/main", "oid": "authored"}}
	if need, err := gqlTestInspect(t, query, variables, "Write"); err != nil || !need {
		t.Fatal("selected aliased variable mutation hid an outside ref", err)
	}
	if _, err := gqlTestInspect(t, query, variables, ""); err == nil {
		t.Fatal("ambiguous operation document was accepted")
	}
	if _, err := gqlTestInspect(t, query, nil, "Write"); err == nil {
		t.Fatal("missing required mutation input was accepted")
	}
	defaulted := `mutation M($name:String!="refs/heads/` + prefix + `fix"){...F} fragment F on Mutation { renamed:createRef(input:{repositoryId:"repo",name:$name,oid:"authored"}){clientMutationId}}`
	if need, err := gqlTestInspect(t, defaulted, nil, "M"); err != nil || need {
		t.Fatal("variable default/fragment alias was not resolved", err)
	}
	if need, err := gqlTestInspect(t, defaulted, map[string]any{"name": "refs/heads/main"}, "M"); err != nil || !need {
		t.Fatal("provided input failed to replace a safe default", err)
	}
	skipped := `mutation M($skip:Boolean!){... on Mutation { createRef(input:{repositoryId:"repo",name:"refs/heads/main",oid:"authored"}) @skip(if:$skip){clientMutationId}}}`
	if need, err := gqlTestInspect(t, skipped, map[string]any{"skip": true}, "M"); err != nil || need {
		t.Fatal("resolved directive semantics changed", err)
	}
	if need, err := gqlTestInspect(t, skipped, map[string]any{"skip": false}, "M"); err != nil || !need {
		t.Fatal("conditional outside ref escaped required authority", err)
	}
	if need, err := gqlTestInspect(t, `mutation { addComment(input:{subjectId:"opaque",body:"authored"}){clientMutationId}}`, nil, ""); err != nil || need {
		t.Fatal("the ref row became an unapproved global mutation gate", err)
	}
}

func TestGraphQLQuotedBlockFixedPairVariableWidthAndUTF8Strings(t *testing.T) {
	prefix := "refs/heads/" + brand.Name + "/model234567/"
	for _, literal := range []string{`"` + prefix + `fix-💩"`, `"` + prefix + `fix-\uD83D\uDCA9"`, `"` + prefix + `fix-\u{1F4A9}"`, `"""` + "\n  " + prefix + "fix-💩\n" + `"""`} {
		query := `mutation { createRef(input:{repositoryId:"repo",name:` + literal + `,oid:"authored"}){clientMutationId}}`
		if need, err := gqlTestInspect(t, query, nil, ""); err != nil || need {
			t.Fatal("equivalent valid GraphQL scalar representations diverged", err)
		}
	}
	for _, escape := range []string{`\uD83D`, `\uDCA9`, `\uD83D\u0041`, `\u{D800}`, `\u{110000}`, `\u{}`, `\x41`, `\u{1234567}`} {
		query := `mutation { createRef(input:{repositoryId:"repo",name:"` + prefix + escape + `",oid:"authored"}){clientMutationId}}`
		if _, err := gqlTestInspect(t, query, nil, ""); err == nil {
			t.Fatal("malformed or non-scalar GraphQL escape reached service admission")
		}
	}
}

func TestGraphQLRepeatedFragmentsMemoisePerSelectedRequestAndCancel(t *testing.T) {
	var source strings.Builder
	source.WriteString(`mutation M($name:String!){...F30} fragment F0 on Mutation { createRef(input:{repositoryId:"repo",name:$name,oid:"authored"}){clientMutationId}}`)
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&source, " fragment F%d on Mutation { ...F%d ...F%d }", i, i-1, i-1)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request := Request{Host: "api.github.com", Port: 443, Path: "/graphql", Method: "POST"}
	body := gqlTestBody(t, source.String(), map[string]any{"name": "refs/heads/" + brand.Name + "/model234567/fix"}, "M")
	if need, err := inspectGraphQLRefWrite(ctx, request, body, brand.Name+"/model234567/"); err != nil || need {
		t.Fatal("compact repeated fragment chain expanded instead of classifying once", err)
	}
	if need, err := gqlTestInspect(t, source.String(), map[string]any{"name": "refs/heads/main"}, "M"); err != nil || !need {
		t.Fatal("fragment memo escaped its selected request/variable binding", err)
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := inspectGraphQLRefWrite(cancelled, request, body, brand.Name+"/model234567/"); !errors.Is(err, context.Canceled) {
		t.Fatal("GraphQL classification ignored cancellation", err)
	}
	if _, err := gqlTestInspect(t, `mutation{...F} fragment F on Mutation{...F}`, nil, ""); err == nil {
		t.Fatal("fragment cycle was accepted")
	}
}

func TestGraphQLActualHTTPShapeFailureNeverSpendsEndpointLease(t *testing.T) {
	policy := api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.github.com"}, {Layer: api.LayerSession, Allow: true, Target: "api.github.com/graphql", Once: true, GrantID: "graphql-endpoint"}}}
	f := newModelHTTPFixture(t, "shell", policy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "body", 500)
			return
		}
		w.WriteHeader(200)
	}))
	for _, query := range []string{`mutation{createRef(input:{repositoryId:"repo",name:1,oid:"authored"}){clientMutationId}}`, `mutation{createRef(input:{repositoryId:"repo",name:"refs/heads/main",Name:"other",oid:"authored"}){clientMutationId}}`, `mutation{...Missing}`, `mutation{createRef(input:{repositoryId:"repo",name:"bad\u{D800}",oid:"authored"}){clientMutationId}}`} {
		body := gqlTestBody(t, query, nil, "")
		before := snapshotDNSLedger(f.env.srv.currentEngine())
		req := httptest.NewRequest("POST", "http://api.github.com:443/graphql", strings.NewReader(string(body))).WithContext(t.Context())
		req.Body = &decoyLedgerProbeBody{t: t, engine: f.env.srv.currentEngine(), reader: strings.NewReader(string(body))}
		writer := httptest.NewRecorder()
		f.env.srv.proxyHTTPRequest(writer, req, StreamHeader{Via: "explicit", Host: "api.github.com", Port: 443}, "http", nil)
		if writer.Code != 403 || f.lookups.Load() != 0 || f.dials.Load() != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(f.env.srv.currentEngine())) {
			t.Fatal("malformed guarded GraphQL input reached an origin or spent its endpoint lease")
		}
	}
	valid := gqlTestBody(t, `mutation{createRef(input:{repositoryId:"repo",name:"refs/heads/main",oid:"authored"}){clientMutationId}}`, nil, "")
	response, _ := f.request(t, "POST", "api.github.com", "/graphql", valid, "")
	if response.Code != 200 || f.requests.Load() != 1 {
		t.Fatal("explicit legacy endpoint authority did not open a parseable outside ref")
	}
	response, _ = f.request(t, "POST", "api.github.com", "/graphql", valid, "")
	if response.Code != 403 || f.requests.Load() != 1 {
		t.Fatal("a spent endpoint lease fell back to broad host authority")
	}
}
