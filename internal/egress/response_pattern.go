package egress

import (
	"errors"
	"regexp"
	"regexp/syntax"
	"sort"
)

const responsePatternWindow = 170

// Main supplies pinned immutable rule data. This helper owns no rule registry,
// binding, generation, destination grant or public data file.
type responseMintRule struct{ ID, Expression string }

type responseRuleSnapshot struct {
	rules []responseRuleProgram
	first [256]bool
}
type responseRuleProgram struct {
	id      string
	re      *regexp.Regexp
	prog    *syntax.Prog
	maximum int
	prefix  string
	first   [256]bool
}

var errResponseRules = errors.New("invalid finite response credential rules. Try: update matching helpers")

func newResponseRuleSnapshot(rules []responseMintRule) (*responseRuleSnapshot, error) {
	snapshot := new(responseRuleSnapshot)
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if rule.ID == "" || seen[rule.ID] {
			return nil, errResponseRules
		}
		seen[rule.ID] = true
		ast, err := syntax.Parse(rule.Expression, syntax.Perl)
		if err != nil || ast.MaxCap() != 1 || !responseLeadingCapture(ast) {
			return nil, errResponseRules
		}
		maximum, ok := responseASCIIWidth(ast)
		if !ok || maximum == 0 || maximum >= responsePatternWindow {
			return nil, errResponseRules
		}
		compiled, err := regexp.Compile(rule.Expression)
		if err != nil {
			return nil, errResponseRules
		}
		program, err := syntax.Compile(ast.Simplify())
		if err != nil {
			return nil, errResponseRules
		}
		capture := responseCaptureNode(ast)
		if capture == nil {
			return nil, errResponseRules
		}
		captureExpression, err := regexp.Compile(capture.String())
		if err != nil {
			return nil, errResponseRules
		}
		prefix, _ := captureExpression.LiteralPrefix()
		item := responseRuleProgram{id: rule.ID, re: compiled, prog: program, maximum: maximum, prefix: prefix}
		for b := range item.first {
			item.first[b] = item.viable([]byte{byte(b)}, false, 0, false)
			snapshot.first[b] = snapshot.first[b] || item.first[b]
		}
		snapshot.rules = append(snapshot.rules, item)
	}
	sort.Slice(snapshot.rules, func(i, j int) bool { return snapshot.rules[i].id < snapshot.rules[j].id })
	return snapshot, nil
}

func responseCaptureNode(node *syntax.Regexp) *syntax.Regexp {
	if node.Op == syntax.OpCapture && node.Cap == 1 {
		return node.Sub[0]
	}
	for _, sub := range node.Sub {
		if found := responseCaptureNode(sub); found != nil {
			return found
		}
	}
	return nil
}

func responseLeadingCapture(node *syntax.Regexp) bool {
	if node.Op == syntax.OpCapture {
		return node.Cap == 1 && !responseNullable(node.Sub[0])
	}
	if node.Op != syntax.OpConcat {
		return false
	}
	for _, sub := range node.Sub {
		switch sub.Op {
		case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		default:
			return responseLeadingCapture(sub)
		}
	}
	return false
}

func responseNullable(node *syntax.Regexp) bool {
	switch node.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary, syntax.OpQuest:
		return true
	case syntax.OpCapture:
		return responseNullable(node.Sub[0])
	case syntax.OpLiteral:
		return len(node.Rune) == 0
	case syntax.OpConcat:
		for _, sub := range node.Sub {
			if !responseNullable(sub) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range node.Sub {
			if responseNullable(sub) {
				return true
			}
		}
		return false
	case syntax.OpRepeat:
		return node.Min == 0 || responseNullable(node.Sub[0])
	default:
		return false
	}
}

func responseASCIIWidth(node *syntax.Regexp) (int, bool) {
	switch node.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0, true
	case syntax.OpLiteral:
		if node.Flags&syntax.FoldCase != 0 {
			return 0, false
		}
		for _, r := range node.Rune {
			if r > 127 {
				return 0, false
			}
		}
		return len(node.Rune), true
	case syntax.OpCharClass:
		for _, r := range node.Rune {
			if r > 127 {
				return 0, false
			}
		}
		return 1, true
	case syntax.OpCapture, syntax.OpQuest:
		return responseASCIIWidth(node.Sub[0])
	case syntax.OpConcat:
		total := 0
		for _, sub := range node.Sub {
			n, ok := responseASCIIWidth(sub)
			if !ok || n >= responsePatternWindow-total {
				return 0, false
			}
			total += n
		}
		return total, true
	case syntax.OpAlternate:
		maximum := 0
		for _, sub := range node.Sub {
			n, ok := responseASCIIWidth(sub)
			if !ok {
				return 0, false
			}
			maximum = max(maximum, n)
		}
		return maximum, true
	case syntax.OpRepeat:
		if node.Max < 0 || node.Max >= responsePatternWindow {
			return 0, false
		}
		n, ok := responseASCIIWidth(node.Sub[0])
		if !ok || n != 0 && node.Max >= (responsePatternWindow+n-1)/n {
			return 0, false
		}
		return n * node.Max, true
	default:
		return 0, false
	}
}

// closure uses standard regexp instruction and empty-context definitions.
// Rules are finite ASCII, so each consumed source byte has one transition.
func (r *responseRuleProgram) closure(starts []uint32, previous, next rune) []uint32 {
	stack := append([]uint32(nil), starts...)
	seen := make([]bool, len(r.prog.Inst))
	active := make([]uint32, 0, len(starts))
	context := syntax.EmptyOpContext(previous, next)
	for len(stack) > 0 {
		pc := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[pc] {
			continue
		}
		seen[pc] = true
		instruction := &r.prog.Inst[pc]
		switch instruction.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			stack = append(stack, instruction.Out, instruction.Arg)
		case syntax.InstCapture, syntax.InstNop:
			stack = append(stack, instruction.Out)
		case syntax.InstEmptyWidth:
			if syntax.EmptyOp(instruction.Arg)&context == syntax.EmptyOp(instruction.Arg) {
				stack = append(stack, instruction.Out)
			}
		case syntax.InstFail:
		default:
			active = append(active, pc)
		}
	}
	return active
}

func (r *responseRuleProgram) viable(data []byte, eof bool, previous byte, hasPrevious bool) bool {
	if len(data) > r.maximum {
		return false
	}
	states := []uint32{uint32(r.prog.Start)}
	before := rune(-1)
	if hasPrevious {
		before = rune(previous)
	}
	for _, b := range data {
		active := r.closure(states, before, rune(b))
		states = nil
		for _, pc := range active {
			instruction := &r.prog.Inst[pc]
			if (instruction.Op == syntax.InstRune || instruction.Op == syntax.InstRune1) && instruction.MatchRune(rune(b)) {
				states = append(states, instruction.Out)
			}
		}
		if len(states) == 0 {
			return false
		}
		before = rune(b)
	}
	if eof {
		for _, pc := range r.closure(states, before, -1) {
			if r.prog.Inst[pc].Op == syntax.InstMatch {
				return true
			}
		}
		return false
	}
	// At a read boundary next input is unknown. Explore its three ASCII
	// boundary classes without claiming any of them is actual stream EOF.
	for _, next := range []rune{-1, 'a', '!', '\n'} {
		if len(r.closure(states, before, next)) != 0 {
			return true
		}
	}
	return false
}

type responseMintMatch struct {
	length int
	ruleID string
	wait   bool
}

func (s *responseRuleSnapshot) match(data []byte, eof bool, previous byte, hasPrevious bool) responseMintMatch {
	var result responseMintMatch
	if s == nil || len(data) == 0 {
		return result
	}
	for i := range s.rules {
		rule := &s.rules[i]
		if !rule.first[data[0]] {
			continue
		}
		prefixMatches := true
		for p := 0; p < min(len(data), len(rule.prefix)); p++ {
			if data[p] != rule.prefix[p] {
				prefixMatches = false
				break
			}
		}
		if !prefixMatches {
			continue
		}
		n := min(len(data), rule.maximum+1)
		pad := 0
		subject := make([]byte, 0, n+1)
		if hasPrevious {
			subject = append(subject, previous)
			pad = 1
		}
		subject = append(subject, data[:n]...)
		indexes := rule.re.FindSubmatchIndex(subject)
		if len(indexes) == 4 && indexes[0] == pad && indexes[2] == pad {
			observedEnd := indexes[1] > indexes[3]
			trueEnd := eof && n == len(data)
			if observedEnd || trueEnd || indexes[1] < len(subject) {
				length := indexes[3] - pad
				if length > result.length {
					result.length = length
					result.ruleID = rule.id
				}
				continue
			}
		}
		if !eof && rule.viable(data, false, previous, hasPrevious) {
			result.wait = true
		}
	}
	return result
}
