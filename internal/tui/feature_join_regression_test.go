package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestPendingAttachUsesTheSettingsReadAtExecution(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "broken-keeps-last"}[broken], func(t *testing.T) {
			be := newFakeBackend()
			old := be.Settings()
			old.Attach.DetachKey = "ctrl+q"
			be.settings = &old
			m := New(context.Background(), be, Options{})
			defer m.Close()
			h := client.Host{Name: "box", SSH: "box"}
			pa := &pendingAttach{settingsRead: m.beginSettingsRead(), host: h.Name, id: "abcd2345", opts: client.AttachOptions{Host: h, Session: "abcd2345", State: m.attach, Settings: m.settings}}
			m.pendingAttach = pa
			fresh := old
			fresh.Attach.DetachKey = "ctrl+t"
			be.settings = &fresh
			if broken {
				be.settingsWarn = "config.toml has a broken value"
			}
			program, err := m.runPendingAttach(m.ctx, nil, nil, false)
			if err != nil || program == nil || len(be.attached) != 1 {
				t.Fatalf("pending attach = %v, %v, calls %d", program, err, len(be.attached))
			}
			want := fresh.Attach.DetachKey
			if broken {
				want = old.Attach.DetachKey
			}
			if got := be.attached[0].Settings.Attach.DetachKey; got != want {
				t.Fatalf("actual attach used detach key %q, want %q", got, want)
			}
			if broken && pa.warning == "" || !broken && pa.fresh == nil {
				t.Fatal("the actual attach lost the read result for its resumed model")
			}
		})
	}
}

type reconnectCloseGate struct {
	Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *reconnectCloseGate) Close() {
	c.once.Do(func() {
		close(c.entered)
		<-c.release
		c.Conn.Close()
	})
}

func TestSupersededReconnectCannotRetireTheNewConnection(t *testing.T) {
	be := newFakeBackend()
	host := client.Host{Name: "box", SSH: "box"}
	be.hosts = []client.Host{host}
	be.state[host.Name] = &fakeHost{}
	m := New(context.Background(), be, Options{})
	defer m.Close()
	base, err := be.Dial(m.ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	gate := &reconnectCloseGate{Conn: base, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(gate.release) })
	h := m.hostByName(host.Name)
	h.conn, h.status = gate, stOnline
	m.conns.add(gate)
	var retired atomic.Int32
	be.retire = func(context.Context, client.Host) error { retired.Add(1); return nil }
	first := m.reconnect(h)
	oldDone := make(chan tea.Msg, 1)
	go func() { oldDone <- first() }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first reconnect did not close its owned old bridge")
	}
	second := m.reconnect(h)
	msg, ok := second().(dialledMsg)
	if !ok || msg.err != nil || msg.conn == nil || msg.gen != h.gen {
		t.Fatalf("new reconnect did not finish: %+v", msg)
	}
	m.onDialled(msg)
	release.Do(func() { close(gate.release) })
	select {
	case stale := <-oldDone:
		if stale != nil {
			t.Fatalf("superseded reconnect published a result: %+v", stale)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("superseded reconnect did not join after its old bridge closed")
	}
	if retired.Load() != 1 || !h.online() || h.needRetire {
		t.Fatalf("stale reconnect retired the new connection: calls=%d online=%v pending=%v", retired.Load(), h.online(), h.needRetire)
	}
}

func TestScreenEditingFloodStaysInsideItsExistingDeadline(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {1000, 500}} {
		for _, cmd := range []string{"S", "T", "L", "M", "@", "P", "X"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], cmd), func(t *testing.T) {
				unit := "x\x1b[999" + cmd
				data := []byte(strings.Repeat(unit, PeekBytes/len(unit)))
				ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
				defer cancel()
				if _, err := ScreenLinesContext(ctx, data, size[0], size[1]); err != nil {
					t.Fatalf("bounded screen flood did not finish inside the existing deadline: %v", err)
				}
			})
		}
	}
}

func TestScreenRenderObservesMidRenderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := onScreenChunk
	onScreenChunk = cancel
	defer func() { onScreenChunk = previous }()
	if lines, err := ScreenLinesContext(ctx, []byte(strings.Repeat("x", PeekBytes)), 80, 24); !errors.Is(err, context.Canceled) || lines != nil {
		t.Fatalf("mid-render cancellation = %v, %v", lines, err)
	}
}

func TestLargeScreenExecutesSingleScrollAndLineEdit(t *testing.T) {
	cases := []struct {
		name  string
		input string
		row0  string
		row1  string
	}{
		{name: "S", input: "top\r\nsecond\x1b[1S", row0: "second", row1: ""},
		{name: "T", input: "top\x1b[1T", row0: "", row1: "top"},
		{name: "L", input: "top\x1b[1;1H\x1b[1L", row0: "", row1: "top"},
		{name: "M", input: "top\r\nsecond\x1b[1;1H\x1b[1M", row0: "second", row1: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines, err := ScreenLinesContext(context.Background(), []byte(tc.input), 1000, 500)
			if err != nil {
				t.Fatalf("large screen %s failed: %v", tc.name, err)
			}
			if len(lines) < 2 || lines[0] != tc.row0 || lines[1] != tc.row1 {
				t.Fatalf("large screen %s got rows [%q, %q], want [%q, %q]", tc.name, lines[0], lines[1], tc.row0, tc.row1)
			}
		})
	}
}
