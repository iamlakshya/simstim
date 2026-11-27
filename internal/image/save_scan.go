package image

import (
	"fmt"
	"io"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// The scanner builds one byte automaton for the original values. Four small
// cursors inspect raw bytes and the three possible byte phases in a standard
// base64 bitstream. No encoded variants or complete file are retained.
type saveScanNode struct {
	next map[byte]int
	fail int
	name string
}

type saveBase64State struct {
	bits              uint16
	count, skip, node int
}

type saveScanner struct {
	nodes   []saveScanNode
	raw     int
	encoded [3]saveBase64State
	path    string
}

func (*saveScanner) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, api.Redacted) }

func newSaveScanner(values map[string]api.Secret) (*saveScanner, error) {
	scanner := &saveScanner{nodes: []saveScanNode{{next: map[byte]int{}}}}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if name == "" || api.CleanText(name, 256) != name {
			return nil, saveFailure("image_bad_layer", "the original image scan has an invalid secret name")
		}
		value, node := values[name], 0
		if value == "" {
			continue
		}
		for index := 0; index < len(value); index++ {
			next, found := scanner.nodes[node].next[value[index]]
			if !found {
				next = len(scanner.nodes)
				scanner.nodes[node].next[value[index]] = next
				scanner.nodes = append(scanner.nodes, saveScanNode{next: map[byte]int{}})
			}
			node = next
		}
		if scanner.nodes[node].name == "" {
			scanner.nodes[node].name = name
		}
	}
	queue := make([]int, 0, len(scanner.nodes))
	for _, node := range scanner.nodes[0].next {
		queue = append(queue, node)
	}
	for head := 0; head < len(queue); head++ {
		node := queue[head]
		for character, child := range scanner.nodes[node].next {
			failure := scanner.nodes[node].fail
			for failure != 0 {
				if _, found := scanner.nodes[failure].next[character]; found {
					break
				}
				failure = scanner.nodes[failure].fail
			}
			if next, found := scanner.nodes[failure].next[character]; found {
				scanner.nodes[child].fail = next
			}
			if scanner.nodes[child].name == "" {
				scanner.nodes[child].name = scanner.nodes[scanner.nodes[child].fail].name
			}
			queue = append(queue, child)
		}
	}
	scanner.reset("")
	return scanner, nil
}

func (scanner *saveScanner) reset(name string) {
	scanner.path, scanner.raw = name, 0
	for index := range scanner.encoded {
		scanner.encoded[index] = saveBase64State{skip: 2 * index}
	}
}

func (scanner *saveScanner) clear() {
	for index := range scanner.nodes {
		clear(scanner.nodes[index].next)
		scanner.nodes[index] = saveScanNode{}
	}
	clear(scanner.nodes)
	scanner.nodes = nil
	scanner.reset("")
}

func (scanner *saveScanner) advance(node int, character byte) (int, string) {
	for node != 0 {
		if _, found := scanner.nodes[node].next[character]; found {
			break
		}
		node = scanner.nodes[node].fail
	}
	if next, found := scanner.nodes[node].next[character]; found {
		node = next
	}
	return node, scanner.nodes[node].name
}

func saveBase64Digit(character byte) (uint16, bool) {
	switch {
	case character >= 'A' && character <= 'Z':
		return uint16(character - 'A'), true
	case character >= 'a' && character <= 'z':
		return uint16(character - 'a' + 26), true
	case character >= '0' && character <= '9':
		return uint16(character - '0' + 52), true
	case character == '+':
		return 62, true
	case character == '/':
		return 63, true
	default:
		return 0, false
	}
}

func (scanner *saveScanner) Write(data []byte) (int, error) {
	for offset, character := range data {
		var name string
		scanner.raw, name = scanner.advance(scanner.raw, character)
		if name != "" {
			return offset, scanner.found(name)
		}
		digit, encoded := saveBase64Digit(character)
		if !encoded {
			for index := range scanner.encoded {
				scanner.encoded[index] = saveBase64State{skip: 2 * index}
			}
			continue
		}
		for index := range scanner.encoded {
			state := &scanner.encoded[index]
			bits := 6 - state.skip
			state.bits = state.bits<<bits | digit&uint16((1<<bits)-1)
			state.count += bits
			state.skip = 0
			if state.count < 8 {
				continue
			}
			state.count -= 8
			decoded := byte(state.bits >> state.count)
			state.bits &= uint16((1 << state.count) - 1)
			state.node, name = scanner.advance(state.node, decoded)
			if name != "" {
				return offset, scanner.found(name)
			}
		}
	}
	return len(data), nil
}

func (scanner *saveScanner) found(name string) error {
	return saveFailure("image_secret", fmt.Sprintf("%s holds the value of %s, so image save stopped and kept nothing", scanner.path, name))
}
