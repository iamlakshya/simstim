package tui

import (
	"context"
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

// fakeHostKey is the fingerprint of every fake host's key.
var fakeHostKey = "SHA256:" + strings.Repeat("k", 43)

func (b *fakeBackend) StagePin(ctx context.Context, name string) error {
	if gate := b.stageGate; gate != nil {
		b.blocked.Add(1)
		defer b.blocked.Add(-1)
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.record("stage %s", name)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.staged == nil {
		b.staged = map[string]string{}
	}
	b.staged[name] = ""
	return nil
}

func (b *fakeBackend) PinHostKey(_ context.Context, h client.Host, want string) (client.Pin, error) {
	b.record("pin %s %s", h.Name, want)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pinErr != nil {
		return client.Pin{}, b.pinErr
	}
	p := client.Pin{Fingerprint: fakeHostKey, Type: "ssh-ed25519", Keys: 1, Source: client.PinFromKnownHosts}
	switch {
	case want != "" && want != fakeHostKey:
		return client.Pin{}, &client.UserError{Msg: h.Name + " shows another key", Code: "host_key_mismatch", Kind: client.KindRefused}
	case want != "":
		p.Source = client.PinMatchedFlag
	case b.pinUnknown:
		return client.Pin{}, &client.UncheckedKeyError{Pin: client.Pin{Fingerprint: fakeHostKey, Type: "ssh-ed25519", Keys: 1}}
	}
	if _, ok := b.staged[h.Name]; ok {
		b.staged[h.Name] = p.Fingerprint
	}
	return p, nil
}

// SaveHostPin saves h, as the real one does, and records the pin that it
// saves with it.
func (b *fakeBackend) SaveHostPin(h client.Host) error {
	return b.SaveHostPinContext(context.Background(), h)
}

func (b *fakeBackend) SaveHostPinContext(ctx context.Context, h client.Host) error {
	hosts, err := b.LoadHosts(ctx)
	if err != nil {
		return err
	}
	var was *client.Host
	for i := range hosts {
		if hosts[i].Name == h.Name {
			was = &hosts[i]
		}
	}
	_, err = b.UpdateHostsContext(ctx, client.HostChange{Kind: "host save", Was: was, Now: &h, Pin: true})
	return err
}

func (b *fakeBackend) ForgetHost(ctx context.Context, name string) error {
	hosts, err := b.LoadHosts(ctx)
	if err != nil {
		return err
	}
	var was *client.Host
	for i := range hosts {
		if hosts[i].Name == name {
			was = &hosts[i]
		}
	}
	if was == nil {
		return nil
	}
	_, err = b.UpdateHostsContext(ctx, client.HostChange{Kind: "host forget", Was: was})
	return err
}

func (b *fakeBackend) AbortPin(name string) {
	b.mu.Lock()
	_, ok := b.staged[name]
	delete(b.staged, name)
	b.mu.Unlock()
	if ok {
		b.record("abort %s", name)
	}
}
