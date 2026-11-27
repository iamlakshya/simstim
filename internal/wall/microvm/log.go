package microvm

import (
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

const vmmLogBytes = 64 << 10

type byteTail struct {
	mu   sync.Mutex
	data []byte
}

func (t *byteTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if n >= vmmLogBytes {
		t.data = append(t.data[:0], p[n-vmmLogBytes:]...)
		return n, nil
	}
	if len(t.data)+n > vmmLogBytes {
		drop := len(t.data) + n - vmmLogBytes
		copy(t.data, t.data[drop:])
		t.data = t.data[:len(t.data)-drop]
	}
	t.data = append(t.data, p...)
	return n, nil
}
func (t *byteTail) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	text := strings.TrimSpace(string(t.data))
	if index := strings.LastIndexByte(text, '\n'); index >= 0 {
		text = text[index+1:]
	}
	return api.CleanText(text, 512)
}

func (t *byteTail) snapshot() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.data...)
}
