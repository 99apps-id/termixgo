package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/99apps-id/termixgo/internal/agent"
	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/cron"
	"github.com/99apps-id/termixgo/internal/telegram"
)

// heartbeatDefaultPrompt is the periodic self-check. The sentinel is what keeps
// a quiet assistant from pinging every interval.
const heartbeatDefaultPrompt = "This is a periodic heartbeat of the Termixgo assistant. " +
	"If there is nothing worth telling the operator, reply with exactly HEARTBEAT_OK and nothing else. " +
	"Otherwise reply with one short, useful update. Use tools only if the workspace HEARTBEAT.md asks for something."

// heartbeatPrompt includes the workspace checklist when one is present.
func heartbeatPrompt(workspace string) string {
	data, err := os.ReadFile(filepath.Join(workspace, "HEARTBEAT.md"))
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return heartbeatDefaultPrompt
	}
	return heartbeatDefaultPrompt + "\n\nHEARTBEAT.md:\n" + strings.TrimSpace(string(data))
}

// parseHeartbeatInterval accepts a Go duration and floors a too-short one.
func parseHeartbeatInterval(value string) time.Duration {
	interval, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || interval < time.Minute {
		return 30 * time.Minute
	}
	return interval
}

// CronStore opens the scheduled-job file, once per process.
func (a *App) CronStore() (*cron.Store, error) {
	a.mu.Lock()
	if a.cronStore != nil {
		store := a.cronStore
		a.mu.Unlock()
		return store, nil
	}
	a.mu.Unlock()

	path, err := cron.DefaultPath()
	if err != nil {
		return nil, err
	}
	store, err := cron.Open(path)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.cronStore == nil {
		a.cronStore = store
	}
	store = a.cronStore
	a.mu.Unlock()
	return store, nil
}

// StartScheduler launches the scheduled jobs and the heartbeat. It is called by
// `serve`, the long-lived process; a terminal session pays nothing for it.
func (a *App) StartScheduler() error {
	a.mu.Lock()
	if a.scheduler != nil {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	store, err := a.CronStore()
	if err != nil {
		return err
	}
	scheduler := &cron.Scheduler{
		Store:   store,
		Runner:  a,
		Deliver: a,
		Log: func(line string) {
			a.mu.Lock()
			a.schedulerStatus = line
			a.mu.Unlock()
		},
	}
	cfg := a.Config()
	if cfg.Heartbeat.Enabled {
		scheduler.SetHeartbeat(&cron.Heartbeat{
			Interval: parseHeartbeatInterval(cfg.Heartbeat.Interval),
			Prompt:   heartbeatPrompt(a.workspace),
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.scheduler = scheduler
	a.schedulerCancel = cancel
	a.mu.Unlock()
	go scheduler.Run(ctx)
	return nil
}

// StopScheduler stops the loop and waits for a job in flight to finish.
func (a *App) StopScheduler() {
	a.mu.Lock()
	cancel := a.schedulerCancel
	scheduler := a.scheduler
	a.scheduler = nil
	a.schedulerCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if scheduler != nil {
		scheduler.Wait()
	}
}

// RunScheduled runs one isolated turn for a scheduled job. It shares the model,
// budget and tool wiring, but uses a throwaway session so a scheduled run never
// becomes the operator's conversation.
func (a *App) RunScheduled(ctx context.Context, prompt string, progress func(string)) (string, error) {
	if progress != nil {
		progress("Working...")
		claim := a.SetObserver(func(event agent.Event) {
			if line := telegramProgressLine(event); line != "" {
				progress(line)
			}
		})
		defer a.ClearObserver(claim)
	}
	session := agent.NewSession(a.workspace, a.CurrentModel().ID)
	if err := a.runOn(ctx, prompt, nil, session); err != nil {
		if errors.Is(err, ErrBusy) {
			// Surface it as the scheduler's busy sentinel so the job is requeued
			// rather than marked failed.
			return "", cron.ErrBusy
		}
		return "", err
	}
	return session.LastAssistantText(), nil
}

// Deliver is the scheduler's delivery seam: a scheduled result goes to the
// same chat an operator message would answer in.
func (a *App) Deliver(ctx context.Context, text string) error {
	return a.Notify(ctx, text)
}

// Notify posts a message to the paired Telegram chat, whether or not the bot is
// currently polling in this process.
func (a *App) Notify(ctx context.Context, text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	a.mu.Lock()
	bot := a.bot
	chatID := a.cfg.Telegram.ChatID
	a.mu.Unlock()
	if bot != nil && bot.Paired() {
		return bot.Send(ctx, trimmed)
	}
	if chatID == 0 {
		return fmt.Errorf("no Telegram chat is paired")
	}
	token := a.TelegramToken()
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("no bot token is stored")
	}
	client := telegram.NewClient(token)
	_, err := client.SendMarkdown(ctx, chatID, trimmed, nil)
	return err
}

// CronJobs lists the scheduled jobs.
func (a *App) CronJobs() ([]cron.Job, error) {
	store, err := a.CronStore()
	if err != nil {
		return nil, err
	}
	return store.List(), nil
}

// CronAdd parses a schedule spec and stores a job.
func (a *App) CronAdd(name, spec, prompt string) (cron.Job, error) {
	schedule, err := cron.Parse(spec)
	if err != nil {
		return cron.Job{}, err
	}
	store, err := a.CronStore()
	if err != nil {
		return cron.Job{}, err
	}
	return store.Add(name, prompt, schedule, time.Now())
}

// CronRemove deletes a job by id.
func (a *App) CronRemove(id string) (bool, error) {
	store, err := a.CronStore()
	if err != nil {
		return false, err
	}
	return store.Remove(id)
}

// CronSetEnabled toggles a job.
func (a *App) CronSetEnabled(id string, enabled bool) (cron.Job, bool, error) {
	store, err := a.CronStore()
	if err != nil {
		return cron.Job{}, false, err
	}
	return store.SetEnabled(id, enabled, time.Now())
}

// CronJob returns one scheduled job.
func (a *App) CronJob(id string) (cron.Job, bool, error) {
	store, err := a.CronStore()
	if err != nil {
		return cron.Job{}, false, err
	}
	job, ok := store.Get(id)
	return job, ok, nil
}

// CronRun runs a job now and returns its answer, without delivering it.
func (a *App) CronRun(ctx context.Context, id string) (string, error) {
	store, err := a.CronStore()
	if err != nil {
		return "", err
	}
	job, ok := store.Get(id)
	if !ok {
		return "", fmt.Errorf("no job with id %q", id)
	}
	return a.RunScheduled(ctx, job.Prompt, nil)
}

// HeartbeatStatus summarises the heartbeat for the status view.
func (a *App) HeartbeatStatus() string {
	cfg := a.Config()
	if !cfg.Heartbeat.Enabled {
		return "off"
	}
	interval := strings.TrimSpace(cfg.Heartbeat.Interval)
	if interval == "" {
		interval = "30m"
	}
	return "every " + interval
}

// SetHeartbeat turns the heartbeat on or off. It applies from the next start of
// the scheduler, which is what `serve` does at boot.
func (a *App) SetHeartbeat(enabled bool, interval string) error {
	return a.UpdateConfig(func(cfg *config.Config) {
		cfg.Heartbeat.Enabled = enabled
		if strings.TrimSpace(interval) != "" {
			cfg.Heartbeat.Interval = strings.TrimSpace(interval)
		}
	})
}
