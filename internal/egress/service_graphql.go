package egress

import (
	"context"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

func gqlJSONString(p *api.JSONProjection, span api.JSONSpan) (string, error) {
	var value strings.Builder
	err := p.WalkString(span, func(r rune) error { value.WriteRune(r); return nil })
	return value.String(), err
}
func gqlValidName(name string) bool {
	if name == "" || !gqlNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !gqlNameByte(name[i]) {
			return false
		}
	}
	return true
}

func gqlJSONValue(p *api.JSONProjection, body []byte, span api.JSONSpan, depth int) (gqlValue, error) {
	if depth > gqlDepth || span.Start < 0 || span.End <= span.Start || span.End > int64(len(body)) {
		return gqlValue{}, errServiceShape
	}
	value := gqlValue{kind: body[span.Start]}
	switch value.kind {
	case '"':
		value.kind = 'S'
		text, err := gqlJSONString(p, span)
		value.text = text
		return value, err
	case '{':
		value.object = make(map[string]gqlValue)
		cursor, err := p.Fields(span)
		if err != nil {
			return value, err
		}
		for {
			field, ok, err := cursor.Next()
			if err != nil {
				return value, err
			}
			if !ok {
				break
			}
			name, err := gqlJSONString(p, field.Name)
			if err != nil || !gqlValidName(name) {
				return value, errServiceShape
			}
			if _, present := value.object[name]; present {
				return value, errServiceShape
			}
			entry, err := gqlJSONValue(p, body, field.Value, depth+1)
			if err != nil {
				return value, err
			}
			value.object[name] = entry
		}
		return value, nil
	case '[':
		cursor, err := p.Values(span)
		if err != nil {
			return value, err
		}
		for {
			span, ok, err := cursor.Next()
			if err != nil {
				return value, err
			}
			if !ok {
				break
			}
			entry, err := gqlJSONValue(p, body, span, depth+1)
			if err != nil {
				return value, err
			}
			value.list = append(value.list, entry)
		}
		return value, nil
	case 'n':
		value.kind = 'Z'
		return value, nil
	case 't', 'f':
		value.kind = 'B'
		value.text = string(body[span.Start:span.End])
		return value, nil
	default:
		value.kind = 'I'
		value.text = string(body[span.Start:span.End])
		if strings.ContainsAny(value.text, ".eE") {
			value.kind = 'F'
		}
		return value, nil
	}
}

func gqlResolve(ctx context.Context, value gqlValue, provided map[string]gqlValue, definitions map[string]gqlVariable, depth int) (gqlValue, error) {
	if err := ctx.Err(); err != nil {
		return gqlValue{}, err
	}
	if depth > gqlDepth {
		return gqlValue{}, errServiceShape
	}
	if value.kind == '$' {
		definition, exists := definitions[value.text]
		if !exists {
			return gqlValue{}, errServiceShape
		}
		actual, present := provided[value.text]
		if !present && definition.fallback != nil {
			actual, present = *definition.fallback, true
		}
		if !present {
			actual = gqlValue{kind: 'Z'}
		}
		if definition.required && actual.kind == 'Z' {
			return gqlValue{}, errServiceShape
		}
		return actual, nil
	}
	if value.kind == '{' {
		owned := make(map[string]gqlValue, len(value.object))
		for name, child := range value.object {
			resolved, err := gqlResolve(ctx, child, provided, definitions, depth+1)
			if err != nil {
				return gqlValue{}, err
			}
			owned[name] = resolved
		}
		value.object = owned
	}
	if value.kind == '[' {
		owned := make([]gqlValue, len(value.list))
		for i, child := range value.list {
			resolved, err := gqlResolve(ctx, child, provided, definitions, depth+1)
			if err != nil {
				return gqlValue{}, err
			}
			owned[i] = resolved
		}
		value.list = owned
	}
	return value, nil
}

func gqlDirectiveIncluded(ctx context.Context, directives []gqlDirective, provided map[string]gqlValue, definitions map[string]gqlVariable) (bool, error) {
	included := true
	seen := make(map[string]bool)
	for _, directive := range directives {
		if directive.name != "include" && directive.name != "skip" || seen[directive.name] || len(directive.args) != 1 {
			return false, errServiceShape
		}
		seen[directive.name] = true
		input, ok := directive.args["if"]
		if !ok {
			return false, errServiceShape
		}
		value, err := gqlResolve(ctx, input, provided, definitions, 0)
		if err != nil || value.kind != 'B' {
			return false, errServiceShape
		}
		if directive.name == "include" && value.text != "true" || directive.name == "skip" && value.text == "true" {
			included = false
		}
	}
	return included, nil
}

func inspectGraphQLRefWrite(ctx context.Context, request Request, body []byte, prefix string) (bool, error) {
	if ctx == nil {
		return false, errServiceShape
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if request.Method != "POST" || request.query != "" || request.forceQuery {
		return false, errServiceShape
	}
	p, fields, err := serviceBodyFields(ctx, body, []string{"query", "variables", "operationName"})
	if err != nil {
		return false, err
	}
	query, err := serviceString(p, fields, "query", true, true)
	if err != nil || query == "" {
		return false, errServiceShape
	}
	operationName := ""
	if span, exists := fields["operationName"]; exists {
		null, err := p.Literal(span, "null")
		if err != nil {
			return false, err
		}
		if !null {
			operationName, err = gqlJSONString(p, span)
			if err != nil || !gqlValidName(operationName) {
				return false, errServiceShape
			}
		}
	}
	provided := make(map[string]gqlValue)
	if span, exists := fields["variables"]; exists {
		value, err := gqlJSONValue(p, body, span, 0)
		if err != nil {
			return false, err
		}
		if value.kind != 'Z' && value.kind != '{' {
			return false, errServiceShape
		}
		if value.kind == '{' {
			provided = value.object
		}
	}
	document, err := parseGQL(ctx, query)
	if err != nil {
		return false, err
	}
	var operation *gqlOperation
	for i := range document.operations {
		candidate := &document.operations[i]
		if operationName != "" && candidate.name == operationName || operationName == "" && len(document.operations) == 1 {
			operation = candidate
		}
	}
	if operation == nil || operationName != "" && operation.name != operationName {
		return false, errServiceShape
	}
	if len(operation.directives) != 0 {
		return false, errServiceShape
	}
	for name := range operation.variables {
		value := gqlValue{kind: '$', text: name}
		if _, err := gqlResolve(ctx, value, provided, operation.variables, 0); err != nil {
			return false, err
		}
	}
	return inspectGraphQLSelections(ctx, document, operation, provided, prefix)
}

// Results belong to this selected operation and its variables. A fragment is
// classified once; an explicit stack handles long shallow-source dependency
// chains without adding a graph-expansion depth cap or growing the Go stack.
func inspectGraphQLSelections(ctx context.Context, document gqlDocument, operation *gqlOperation, provided map[string]gqlValue, prefix string) (bool, error) {
	memo := make(map[string]gqlFragmentMemo)
	stack := []gqlSelectionFrame{{selections: operation.selections, included: true}}
	fail := func(err error) (bool, error) {
		for _, frame := range stack {
			if frame.fragment != "" {
				memo[frame.fragment] = gqlFragmentMemo{state: 2, err: err}
			}
		}
		return false, err
	}
	expected := "Query"
	if operation.kind == "mutation" {
		expected = "Mutation"
	}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		frame := &stack[len(stack)-1]
		if frame.next == len(frame.selections) {
			need, included, name := frame.needPath, frame.included, frame.fragment
			if name != "" {
				memo[name] = gqlFragmentMemo{state: 2, needPath: need}
			}
			stack[len(stack)-1] = gqlSelectionFrame{}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return need, nil
			}
			if included {
				stack[len(stack)-1].needPath = stack[len(stack)-1].needPath || need
			}
			continue
		}
		selection := frame.selections[frame.next]
		frame.next++
		included, err := gqlDirectiveIncluded(ctx, selection.directives, provided, operation.variables)
		if err != nil {
			return fail(err)
		}
		if selection.spread != "" {
			prior := memo[selection.spread]
			if prior.state == 1 {
				return fail(errServiceShape)
			}
			if prior.state == 2 {
				if prior.err != nil {
					return fail(prior.err)
				}
				if included {
					frame.needPath = frame.needPath || prior.needPath
				}
				continue
			}
			definition, exists := document.fragments[selection.spread]
			if !exists || definition.on != expected || len(definition.directives) != 0 {
				return fail(errServiceShape)
			}
			memo[selection.spread] = gqlFragmentMemo{state: 1}
			stack = append(stack, gqlSelectionFrame{selections: definition.selections, included: included, fragment: selection.spread})
			continue
		}
		if selection.name == "" {
			if selection.on != "" && selection.on != expected {
				return fail(errServiceShape)
			}
			stack = append(stack, gqlSelectionFrame{selections: selection.children, included: included})
			continue
		}
		// Conditions affect authority, not supported guarded-input grammar.
		// Skipped selections still receive the same complete input checks.
		for _, value := range selection.args {
			if _, err := gqlResolve(ctx, value, provided, operation.variables, 0); err != nil {
				return fail(err)
			}
		}
		if operation.kind != "mutation" {
			continue
		}
		required, handled, err := inspectGraphQLMutation(ctx, selection, provided, operation.variables, prefix)
		if err != nil {
			return fail(err)
		}
		if included && handled {
			frame.needPath = frame.needPath || required
		}
	}
	return false, errServiceShape
}

type gqlSelectionFrame struct {
	selections         []gqlSelection
	next               int
	needPath, included bool
	fragment           string
}

type gqlFragmentMemo struct {
	state    byte
	needPath bool
	err      error
}
