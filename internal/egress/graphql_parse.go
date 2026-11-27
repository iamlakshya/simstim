package egress

import (
	"context"
	"unicode/utf8"
)

type gqlValue struct {
	kind   byte
	text   string
	object map[string]gqlValue
	list   []gqlValue
}
type gqlDirective struct {
	name string
	args map[string]gqlValue
}
type gqlSelection struct {
	name, spread, on string
	args             map[string]gqlValue
	directives       []gqlDirective
	children         []gqlSelection
}
type gqlVariable struct {
	required bool
	fallback *gqlValue
}
type gqlOperation struct {
	kind, name string
	variables  map[string]gqlVariable
	directives []gqlDirective
	selections []gqlSelection
}
type gqlFragment struct {
	on         string
	directives []gqlDirective
	selections []gqlSelection
}
type gqlDocument struct {
	operations []gqlOperation
	fragments  map[string]gqlFragment
}
type gqlParser struct{ lexer gqlLexer }

func parseGQL(ctx context.Context, source string) (gqlDocument, error) {
	document := gqlDocument{fragments: make(map[string]gqlFragment)}
	if !utf8.ValidString(source) {
		return document, errServiceShape
	}
	p := gqlParser{lexer: gqlLexer{ctx: ctx, source: source}}
	names := make(map[string]bool)
	for {
		token, err := p.lexer.peek()
		if err != nil {
			return document, err
		}
		if token.kind == 0 {
			break
		}
		if token.kind == 'N' && token.text == "fragment" {
			_, _ = p.lexer.take()
			name, err := p.name()
			if err != nil || name == "on" {
				return document, errServiceShape
			}
			if _, exists := document.fragments[name]; exists {
				return document, errServiceShape
			}
			on, err := p.name()
			if err != nil || on != "on" {
				return document, errServiceShape
			}
			kind, err := p.name()
			if err != nil {
				return document, err
			}
			directives, err := p.directives(0)
			if err != nil {
				return document, err
			}
			selections, err := p.selectionSet(0)
			if err != nil {
				return document, err
			}
			document.fragments[name] = gqlFragment{on: kind, directives: directives, selections: selections}
			continue
		}
		operation := gqlOperation{kind: "query", variables: make(map[string]gqlVariable)}
		if token.kind != '{' {
			operation.kind, err = p.name()
			if err != nil || operation.kind != "query" && operation.kind != "mutation" && operation.kind != "subscription" {
				return document, errServiceShape
			}
			next, err := p.lexer.peek()
			if err != nil {
				return document, err
			}
			if next.kind == 'N' {
				operation.name, err = p.name()
				if err != nil {
					return document, err
				}
			}
			if p.lexer.accept('(') {
				count := 0
				for !p.lexer.accept(')') {
					if _, err := p.lexer.expect('$'); err != nil {
						return document, err
					}
					name, err := p.name()
					if err != nil {
						return document, err
					}
					if _, present := operation.variables[name]; present {
						return document, errServiceShape
					}
					if _, err := p.lexer.expect(':'); err != nil {
						return document, err
					}
					required, err := p.typeName(0)
					if err != nil {
						return document, err
					}
					variable := gqlVariable{required: required}
					if p.lexer.accept('=') {
						value, err := p.value(0, false)
						if err != nil {
							return document, err
						}
						variable.fallback = &value
					}
					if _, err := p.directives(0); err != nil {
						return document, err
					}
					operation.variables[name] = variable
					count++
				}
				if count == 0 {
					return document, errServiceShape
				}
			}
			operation.directives, err = p.directives(0)
			if err != nil {
				return document, err
			}
		}
		operation.selections, err = p.selectionSet(0)
		if err != nil {
			return document, err
		}
		if names[operation.name] {
			return document, errServiceShape
		}
		names[operation.name] = true
		document.operations = append(document.operations, operation)
	}
	if len(document.operations) == 0 {
		return document, errServiceShape
	}
	if len(document.operations) > 1 && names[""] {
		return document, errServiceShape
	}
	return document, nil
}

func (p *gqlParser) name() (string, error) { token, err := p.lexer.expect('N'); return token.text, err }
func (p *gqlParser) typeName(depth int) (bool, error) {
	if depth > gqlDepth {
		return false, errServiceShape
	}
	if p.lexer.accept('[') {
		if _, err := p.typeName(depth + 1); err != nil {
			return false, err
		}
		if _, err := p.lexer.expect(']'); err != nil {
			return false, err
		}
	} else if _, err := p.name(); err != nil {
		return false, err
	}
	return p.lexer.accept('!'), nil
}

func (p *gqlParser) arguments(depth int) (map[string]gqlValue, error) {
	arguments := make(map[string]gqlValue)
	if !p.lexer.accept('(') {
		return arguments, nil
	}
	count := 0
	for !p.lexer.accept(')') {
		name, err := p.name()
		if err != nil {
			return nil, err
		}
		if _, present := arguments[name]; present {
			return nil, errServiceShape
		}
		if _, err := p.lexer.expect(':'); err != nil {
			return nil, err
		}
		value, err := p.value(depth+1, true)
		if err != nil {
			return nil, err
		}
		arguments[name] = value
		count++
	}
	if count == 0 {
		return nil, errServiceShape
	}
	return arguments, nil
}

func (p *gqlParser) directives(depth int) ([]gqlDirective, error) {
	var directives []gqlDirective
	for p.lexer.accept('@') {
		name, err := p.name()
		if err != nil {
			return nil, err
		}
		args, err := p.arguments(depth + 1)
		if err != nil {
			return nil, err
		}
		directives = append(directives, gqlDirective{name: name, args: args})
	}
	return directives, nil
}

func (p *gqlParser) selectionSet(depth int) ([]gqlSelection, error) {
	if depth > gqlDepth {
		return nil, errServiceShape
	}
	if _, err := p.lexer.expect('{'); err != nil {
		return nil, err
	}
	var selections []gqlSelection
	for !p.lexer.accept('}') {
		selection := gqlSelection{}
		if p.lexer.accept('.') {
			token, err := p.lexer.peek()
			if err != nil {
				return nil, err
			}
			if token.kind == 'N' && token.text != "on" {
				selection.spread, err = p.name()
				if err != nil {
					return nil, err
				}
				selection.directives, err = p.directives(depth + 1)
				if err != nil {
					return nil, err
				}
			} else {
				if token.kind == 'N' {
					_, _ = p.lexer.take()
					selection.on, err = p.name()
					if err != nil {
						return nil, err
					}
				}
				selection.directives, err = p.directives(depth + 1)
				if err != nil {
					return nil, err
				}
				selection.children, err = p.selectionSet(depth + 1)
				if err != nil {
					return nil, err
				}
			}
		} else {
			name, err := p.name()
			if err != nil {
				return nil, err
			}
			if p.lexer.accept(':') {
				name, err = p.name()
				if err != nil {
					return nil, err
				}
			}
			selection.name = name
			selection.args, err = p.arguments(depth + 1)
			if err != nil {
				return nil, err
			}
			selection.directives, err = p.directives(depth + 1)
			if err != nil {
				return nil, err
			}
			next, err := p.lexer.peek()
			if err != nil {
				return nil, err
			}
			if next.kind == '{' {
				selection.children, err = p.selectionSet(depth + 1)
				if err != nil {
					return nil, err
				}
			}
		}
		selections = append(selections, selection)
	}
	if len(selections) == 0 {
		return nil, errServiceShape
	}
	return selections, nil
}

func (p *gqlParser) value(depth int, variables bool) (gqlValue, error) {
	if depth > gqlDepth {
		return gqlValue{}, errServiceShape
	}
	token, err := p.lexer.take()
	if err != nil {
		return gqlValue{}, err
	}
	value := gqlValue{kind: token.kind, text: token.text}
	switch token.kind {
	case '$':
		if !variables {
			return value, errServiceShape
		}
		value.text, err = p.name()
		return value, err
	case 'S', 'I', 'F':
		return value, nil
	case 'N':
		value.kind = 'E'
		if token.text == "true" || token.text == "false" {
			value.kind = 'B'
		}
		if token.text == "null" {
			value.kind = 'Z'
		}
		return value, nil
	case '[':
		for !p.lexer.accept(']') {
			entry, err := p.value(depth+1, variables)
			if err != nil {
				return value, err
			}
			value.list = append(value.list, entry)
		}
		return value, nil
	case '{':
		value.object = make(map[string]gqlValue)
		for !p.lexer.accept('}') {
			name, err := p.name()
			if err != nil {
				return value, err
			}
			if _, present := value.object[name]; present {
				return value, errServiceShape
			}
			if _, err := p.lexer.expect(':'); err != nil {
				return value, err
			}
			entry, err := p.value(depth+1, variables)
			if err != nil {
				return value, err
			}
			value.object[name] = entry
		}
		return value, nil
	}
	return value, errServiceShape
}
