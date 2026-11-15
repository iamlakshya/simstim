package host

import (
	"fmt"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/ring"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// runGuestHolder uses the ONE existing Holder/start/read/wait backend. Only
// its guest-local files/events and actual inherited VM terminal differ.
func runGuestHolder(id string, files GuestHolder) error {
	m, err := LoadManifestFile(files.Manifest)
	if err != nil {
		return fmt.Errorf("load guest holder manifest: %w", err)
	}
	if m.ID != id {
		return UserError("guest_init", "the guest holder record names another session", "Start the holder through its matching guest init.")
	}
	log, err := openHolderLog(filepath.Join(filepath.Dir(files.Manifest), "holder.log"), holderLogMax)
	if err != nil {
		return err
	}
	defer log.Close()
	lines := diag.NewLines()
	h := &Holder{id: id, m: m, guest: &files, log: newHolderLog(log, lines), ring: ring.New(RingSize), clients: map[*hclient]struct{}{}, readDone: make(chan struct{}), loadedName: m.Name}
	h.dbg.lines = lines
	events, err := h.openGuestEvents()
	if err != nil {
		return err
	}
	h.ev = events
	defer events.flush(time.Second)
	KeepOnBrokenPipe()
	if err := h.adoptVMTerminal(); err != nil {
		return err
	}
	m.DebugUntil = nil
	if err := h.listen(); err != nil {
		return err
	}
	restored := wasRestored(m)
	if h.vmTerm == nil {
		h.secretsBeforeStart(restored)
	}
	if err := h.start(); err != nil {
		h.mu.Lock()
		code := 127
		h.m.Status = Status{State: StateErrored, Since: time.Now().UTC(), ExitCode: &code, Reason: err.Error()}
		copy := h.copyForSave()
		h.mu.Unlock()
		_ = h.save(copy, false)
		h.closeSockets()
		return err
	}
	h.watchGuest()
	h.startAlerts(restored)
	h.startVT()
	go h.acceptLoop(h.ln, false)
	go h.acceptLoop(h.hookLn, true)
	go h.readLoop()
	go h.waitLoop()
	go h.tick()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	received := <-signals
	h.log.Info("guest holder stopping", "signal", received.String())
	h.stop("stopped with its guest init", true)
	return nil
}
