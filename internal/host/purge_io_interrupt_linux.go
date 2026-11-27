//go:build linux

package host

import (
	"errors"
	"net"
	"sync"
	"time"
)

// An interrupt is diagnostic I/O, never native retirement. Cache its FIRST
// actual result so a retry cannot replace it with a later ErrClosed outcome.
type purgeIOInterruptResultV4 struct {
	once       sync.Once
	done       chan struct{}
	diagnostic error
}
type purgeIOInterruptFactsV4 struct {
	mu        sync.Mutex
	lines     map[*StartupLineV1]*purgeIOInterruptResultV4
	listeners map[*net.UnixListener]*purgeIOInterruptResultV4
}

func (owner *purgeNativeRetirementsV4) interruptLineV4(line *StartupLineV1) {
	if line == nil {
		return
	}
	owner.interrupts.mu.Lock()
	if owner.interrupts.lines == nil {
		owner.interrupts.lines = make(map[*StartupLineV1]*purgeIOInterruptResultV4)
	}
	result := owner.interrupts.lines[line]
	if result == nil {
		result = &purgeIOInterruptResultV4{done: make(chan struct{})}
		owner.interrupts.lines[line] = result
	}
	owner.interrupts.mu.Unlock()
	result.once.Do(func() { defer close(result.done); result.diagnostic = line.SetDeadline(time.Now()) })
}

func (owner *purgeNativeRetirementsV4) interruptListenerV4(listener *net.UnixListener) {
	if listener == nil {
		return
	}
	owner.interrupts.mu.Lock()
	if owner.interrupts.listeners == nil {
		owner.interrupts.listeners = make(map[*net.UnixListener]*purgeIOInterruptResultV4)
	}
	result := owner.interrupts.listeners[listener]
	if result == nil {
		result = &purgeIOInterruptResultV4{done: make(chan struct{})}
		owner.interrupts.listeners[listener] = result
	}
	owner.interrupts.mu.Unlock()
	result.once.Do(func() { defer close(result.done); result.diagnostic = listener.SetDeadline(time.Now()) })
}

func (owner *purgeIOInterruptFactsV4) diagnosticsV4() error {
	owner.mu.Lock()
	lines := make([]*purgeIOInterruptResultV4, 0, len(owner.lines))
	for _, result := range owner.lines {
		lines = append(lines, result)
	}
	for _, result := range owner.listeners {
		lines = append(lines, result)
	}
	owner.mu.Unlock()
	var diagnostic error
	for _, result := range lines {
		// The getter only joins the actual first interrupt. It cannot win its
		// once or replace a not-yet-started native action with an empty one.
		<-result.done
		diagnostic = errors.Join(diagnostic, result.diagnostic)
	}
	return diagnostic
}
