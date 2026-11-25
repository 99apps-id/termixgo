package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
)

type gatewayNotifier struct {
	mu     sync.Mutex
	outbox *gatewayOutbox
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
}

func (a *App) ensureGatewayNotifier() (*gatewayNotifier, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gatewayStopping {
		return nil, fmt.Errorf("gateway notifications are stopping")
	}
	if a.gatewayAlerts != nil {
		return a.gatewayAlerts, nil
	}
	path, err := config.HomePath("gateway-alerts.sqlite")
	if err != nil {
		return nil, err
	}
	outbox, err := openGatewayOutbox(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := &gatewayNotifier{outbox: outbox, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	a.gatewayAlerts = n
	go n.run(a)
	return n, nil
}

func (a *App) stopGatewayNotifier() {
	a.mu.Lock()
	a.gatewayStopping = true
	n := a.gatewayAlerts
	a.gatewayAlerts = nil
	a.mu.Unlock()
	if n == nil {
		return
	}
	n.cancel()
	<-n.done
	n.mu.Lock()
	defer n.mu.Unlock()
	_ = n.outbox.db.Close()
}

func (n *gatewayNotifier) record(a *App, key channel.AccountID, state string) {
	source := key.Channel + "/" + key.Account
	targets := a.gatewayTargets(a.Config())
	n.mu.Lock()
	if n.ctx.Err() != nil {
		n.mu.Unlock()
		return
	}
	emitted, err := n.outbox.transition(source, state, targets)
	if err != nil {
		if deferErr := n.outbox.deferTransition(source, state); deferErr != nil {
			err = fmt.Errorf("%w (also failed to persist retry: %v)", err, deferErr)
		}
	}
	n.mu.Unlock()
	if err != nil {
		a.emit(agent.Event{Kind: agent.EventNotice, Text: "Gateway alert queue: " + err.Error()})
		return
	}
	if emitted {
		select {
		case n.wake <- struct{}{}:
		default:
		}
	}
}

func (n *gatewayNotifier) retryPendingTransitions(a *App) bool {
	targets := a.gatewayTargets(a.Config())
	n.mu.Lock()
	if n.ctx.Err() != nil {
		n.mu.Unlock()
		return false
	}
	pending, err := n.outbox.pendingTransitions()
	if err != nil {
		n.mu.Unlock()
		a.emit(agent.Event{Kind: agent.EventNotice, Text: "Gateway alert queue: " + err.Error()})
		return false
	}
	enqueued := false
	for _, item := range pending {
		if emitted, err := n.outbox.transition(item.source, item.status, targets); err == nil && emitted {
			enqueued = true
		}
	}
	n.mu.Unlock()
	return enqueued
}

func (n *gatewayNotifier) run(a *App) {
	defer close(n.done)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		n.flush(a)
		select {
		case <-n.ctx.Done():
			return
		case <-n.wake:
		case <-ticker.C:
		}
	}
}

func (n *gatewayNotifier) flush(a *App) {
	n.retryPendingTransitions(a)
	n.mu.Lock()
	if n.ctx.Err() != nil {
		n.mu.Unlock()
		return
	}
	deliveries, err := n.outbox.pending()
	n.mu.Unlock()
	if err != nil {
		a.emit(agent.Event{Kind: agent.EventNotice, Text: "Gateway alert queue: " + err.Error()})
		return
	}
	acked := false
	for _, delivery := range deliveries {
		if n.ctx.Err() != nil {
			return
		}
		// Revalidate at send time: a former admin must not receive old alerts.
		cfg := a.Config()
		allowed := false
		for _, target := range a.gatewayTargets(cfg) {
			if target == delivery.target {
				allowed = true
				break
			}
		}
		if !allowed {
			key := channel.AccountID{Channel: delivery.target.channel, Account: delivery.target.account}
			a.mu.Lock()
			runtime := a.channels[key]
			a.mu.Unlock()
			// Without the runtime we cannot revalidate the linked self JID.
			// Keep the delivery until the account comes back.
			if runtime == nil {
				continue
			}
			// A temporarily disabled account is unavailable, not evidence
			// that its admin was revoked. Keep the alert for later.
			if delivery.target.channel == config.ChannelTelegram {
				if account, ok := cfg.TelegramAccount(delivery.target.account); ok && !account.Enabled {
					continue
				}
			} else if delivery.target.channel == config.ChannelWhatsApp {
				if account, ok := cfg.WhatsAppAccount(delivery.target.account); ok && !account.Enabled {
					continue
				}
			}
			n.mu.Lock()
			err := n.outbox.ack(delivery.id)
			n.mu.Unlock()
			if err != nil {
				a.emit(agent.Event{Kind: agent.EventNotice, Text: "Gateway alert queue: " + err.Error()})
			} else {
				acked = true
			}
			continue
		}
		key := channel.AccountID{Channel: delivery.target.channel, Account: delivery.target.account}
		a.mu.Lock()
		runtime := a.channels[key]
		a.mu.Unlock()
		if runtime == nil || runtime.send == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
		err := runtime.send(ctx, delivery.target.user, a.RedactForChannel(delivery.text))
		cancel()
		if err != nil {
			continue
		}
		n.mu.Lock()
		err = n.outbox.ack(delivery.id)
		n.mu.Unlock()
		if err != nil {
			a.emit(agent.Event{Kind: agent.EventNotice, Text: fmt.Sprintf("Gateway alert delivered but not acknowledged: %v", err)})
		} else {
			acked = true
		}
		// Avoid a reconnect burst that sends many old alerts at once.
		select {
		case <-n.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	if acked && n.retryPendingTransitions(a) {
		select {
		case n.wake <- struct{}{}:
		default:
		}
	}
}
