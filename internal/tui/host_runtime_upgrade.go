package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type hostRuntimeUpgradedV1 struct {
	name string
	gen  int
	err  error
}

func (m *Model) upgradeHostRuntimeV1(h *hostConn) tea.Cmd {
	if !h.online() {
		return nil
	}
	upgrader, ok := unclean(h.conn).(interface {
		UpgradeRuntimeOnUse(context.Context, client.RuntimeUpgradeOptions) (api.RuntimeStatus, error)
	})
	if !ok {
		return nil
	}
	name, gen, c := h.name(), h.gen, h.conn
	return m.bgCmd(func(bg context.Context) tea.Msg {
		hasRuntime, err := client.HasReleaseRuntime()
		if err != nil || !hasRuntime {
			return hostRuntimeUpgradedV1{name, gen, err}
		}
		request := client.NewRuntimeUpgradeGrantRequest()
		ctx, cancel := context.WithTimeout(client.WithGrantRequest(bg, request), 5*time.Minute)
		watchDone := make(chan struct{})
		go func() {
			defer close(watchDone)
			select {
			case <-c.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		defer func() { cancel(); <-watchDone }()
		end, err := m.grantOwners.begin(request, ctx, cancel)
		if err != nil {
			return hostRuntimeUpgradedV1{name, gen, errors.Join(err, request.Close())}
		}
		err = m.grantQuestions(ctx, func(owned context.Context) error {
			_, err := upgrader.UpgradeRuntimeOnUse(owned, client.RuntimeUpgradeOptions{})
			return err
		})
		end()
		err = errors.Join(err, m.grantOwners.release(ctx, request))
		return hostRuntimeUpgradedV1{name, gen, err}
	})
}

func (m *Model) onHostRuntimeUpgradedV1(msg hostRuntimeUpgradedV1) tea.Cmd {
	h := m.hostByName(msg.name)
	if h == nil || h.gen != msg.gen || !h.online() {
		return nil
	}
	if msg.err != nil {
		return m.toastErr(msg.err)
	}
	if h.doctoring {
		return m.after(time.Second, msg)
	}
	return m.doctor(h)
}
