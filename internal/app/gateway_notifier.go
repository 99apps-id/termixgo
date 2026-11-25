package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/channel"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/version"
)

// gatewayGraceFor is how long a gateway must stay down before the operator is
// told. A WhatsApp link drops and returns on its own all the time, and an
// expired session needs the operator anyway, so it waits an hour. Telegram
// polling recovers faster, and five minutes is already a real outage. A
// variable so a test can shorten it.
var gatewayGraceFor = func(channelName string) time.Duration {
	if channelName == config.ChannelWhatsApp {
		return time.Hour
	}
	return 5 * time.Minute
}

type gatewayNotifier struct {
	mu     sync.Mutex
	outbox *gatewayOutbox
	// timers hold the offline alerts that are waiting out their grace period,
	// one per source. They are guarded by mu.
	timers map[string]*time.Timer
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
	// Why this process started decides what the first reconnect says.
	if err := outbox.beginRun(version.Version, currentBootTime()); err != nil {
		_ = outbox.db.Close()
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
	n.cancelAllOffline()
	<-n.done
	n.mu.Lock()
	defer n.mu.Unlock()
	_ = n.outbox.db.Close()
}

// record takes a gateway state change. An offline state is held for its grace
// period and dropped if the gateway returns first, so a brief disconnect is
// never an alert. Every other state is applied at once.
func (n *gatewayNotifier) record(a *App, key channel.AccountID, state string) {
	source := key.Channel + "/" + key.Account
	if state == "offline" {
		if grace := gatewayGraceFor(key.Channel); grace > 0 {
			n.armOffline(a, key, source, grace)
			return
		}
	} else {
		n.cancelOffline(source)
	}
	n.recordNow(a, key, state)
}

// armOffline starts the grace period of one source. A second offline report
// while one is already waiting keeps the first start: the outage began then.
func (n *gatewayNotifier) armOffline(a *App, key channel.AccountID, source string, grace time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ctx != nil && n.ctx.Err() != nil {
		return
	}
	if n.timers == nil {
		n.timers = map[string]*time.Timer{}
	}
	if _, waiting := n.timers[source]; waiting {
		return
	}
	n.timers[source] = time.AfterFunc(grace, func() {
		n.mu.Lock()
		delete(n.timers, source)
		n.mu.Unlock()
		n.recordNow(a, key, "offline")
	})
}

func (n *gatewayNotifier) cancelOffline(source string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if timer, ok := n.timers[source]; ok {
		timer.Stop()
		delete(n.timers, source)
	}
}

func (n *gatewayNotifier) cancelAllOffline() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for source, timer := range n.timers {
		timer.Stop()
		delete(n.timers, source)
	}
}

func (n *gatewayNotifier) recordNow(a *App, key channel.AccountID, state string) {
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
	_, _ = n.outbox.purgeStale(time.Now())
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
		if err == nil {
			err = n.outbox.ackSiblings(delivery)
		}
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
