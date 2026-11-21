package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/cron"
)

// Session schedule bounds. A recurring task expires so a forgotten loop does
// not run all week, and a short interval cannot turn into a busy loop.
const (
	maxSessionSchedules = 20
	scheduleExpiry      = 7 * 24 * time.Hour
	minScheduleInterval = time.Minute
)

// SessionScheduler holds the tasks the model scheduled for this process. Each
// one fires as a wake, so it runs as a turn when the terminal is idle and as a
// steer while one runs. Nothing is written to disk; durable jobs go through the
// cron store instead.
type SessionScheduler struct {
	mu     sync.Mutex
	tasks  map[string]*scheduledTask
	nextID int
	emit   Emitter
	now    func() time.Time
}

type scheduledTask struct {
	id       string
	spec     string
	prompt   string
	schedule cron.Schedule
	next     time.Time
	expires  time.Time
	timer    *time.Timer
}

// NewSessionScheduler builds an empty scheduler.
func NewSessionScheduler() *SessionScheduler {
	return &SessionScheduler{tasks: map[string]*scheduledTask{}, now: time.Now}
}

// SetEmitter installs the long-lived sink the tasks fire into.
func (s *SessionScheduler) SetEmitter(emit Emitter) {
	s.mu.Lock()
	s.emit = emit
	s.mu.Unlock()
}

// Add schedules a prompt. It refuses a schedule with no future run and a
// recurrence faster than once a minute.
func (s *SessionScheduler) Add(spec, prompt string) (string, time.Time, error) {
	spec = normalizeScheduleSpec(spec)
	schedule, err := cron.Parse(spec)
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	next, ok := schedule.Next(now)
	if !ok {
		return "", time.Time{}, fmt.Errorf("the schedule has no future run")
	}
	if after, ok := schedule.Next(next); ok && after.Sub(next) < minScheduleInterval {
		return "", time.Time{}, fmt.Errorf("a schedule may repeat at most once a minute")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emit == nil {
		return "", time.Time{}, fmt.Errorf("scheduling needs a front end that can receive notifications")
	}
	if len(s.tasks) >= maxSessionSchedules {
		return "", time.Time{}, fmt.Errorf("already %d scheduled tasks; delete one first", maxSessionSchedules)
	}
	s.nextID++
	task := &scheduledTask{
		id:       fmt.Sprintf("task-%d", s.nextID),
		spec:     strings.TrimSpace(spec),
		prompt:   strings.TrimSpace(prompt),
		schedule: schedule,
		expires:  now.Add(scheduleExpiry),
	}
	s.tasks[task.id] = task
	s.armLocked(task, next)
	return task.id, next, nil
}

func (s *SessionScheduler) armLocked(task *scheduledTask, next time.Time) {
	task.next = next
	task.timer = time.AfterFunc(time.Until(next), func() { s.fire(task.id) })
}

func (s *SessionScheduler) fire(id string) {
	s.mu.Lock()
	task, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	emit := s.emit
	now := s.now()
	next, recurring := task.schedule.Next(now)
	if recurring && next.Before(task.expires) {
		s.armLocked(task, next)
	} else {
		delete(s.tasks, id)
	}
	s.mu.Unlock()
	if emit != nil {
		emit(Event{Kind: EventWake, ToolName: id, Text: fmt.Sprintf(
			"Scheduled task %s (%s) is due:\n%s", id, task.spec, task.prompt)})
	}
}

// Remove cancels one task, reporting whether it existed.
func (s *SessionScheduler) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[strings.TrimSpace(id)]
	if !ok {
		return false
	}
	task.timer.Stop()
	delete(s.tasks, task.id)
	return true
}

// StopAll cancels every task, the shutdown path.
func (s *SessionScheduler) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, task := range s.tasks {
		task.timer.Stop()
		delete(s.tasks, id)
	}
}

// List renders the tasks, soonest first.
func (s *SessionScheduler) List() string {
	s.mu.Lock()
	tasks := make([]*scheduledTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, task)
	}
	s.mu.Unlock()
	if len(tasks) == 0 {
		return "No scheduled tasks in this session."
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].next.Before(tasks[j].next) })
	var out strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&out, "%s  next %s  %s  %s\n", task.id, task.next.Format("2006-01-02 15:04"), task.spec, Shorten(task.prompt, 60))
	}
	return strings.TrimRight(out.String(), "\n")
}

// scheduleTaskTool lets the model schedule a prompt for later: a reminder, a
// recurring check, a follow-up after a deploy.
type scheduleTaskTool struct{}

func (t *scheduleTaskTool) Name() string      { return "schedule_task" }
func (t *scheduleTaskTool) Aliases() []string { return []string{"cron_create", "schedule"} }

// Mutating is true on purpose: a scheduled prompt makes the agent act later
// without anyone watching, and a durable one writes the cron store. Plan mode
// must not create one, and the approval policy should ask first.
func (t *scheduleTaskTool) Mutating() bool { return true }
func (t *scheduleTaskTool) Risk() Risk     { return RiskCommand }
func (t *scheduleTaskTool) Label(a map[string]any) string {
	return "Scheduling " + scheduleLabel(a)
}
func (t *scheduleTaskTool) DoneLabel(a map[string]any) string {
	return "Scheduled " + scheduleLabel(a)
}

func scheduleLabel(a map[string]any) string {
	switch strings.ToLower(argString(a, "action")) {
	case "list":
		return "tasks (list)"
	case "delete":
		return "task removal " + argString(a, "id")
	}
	return argString(a, "schedule") + ": " + Shorten(argString(a, "prompt"), 40)
}

func (t *scheduleTaskTool) Description() string {
	return "Schedule a prompt to run later in this session: a cron expression (\"*/15 * * * *\"), \"every 10m\", \"at 15:30\" or \"at 2026-11-22 09:00\". It fires as a new turn when the terminal is idle. Recurring tasks expire after 7 days. durable stores it as a cron job run by `termixgo serve` instead. Actions: create (default), list, delete."
}
func (t *scheduleTaskTool) Schema() map[string]any {
	return object(map[string]any{
		"action":   strProp("create, list or delete. Defaults to create."),
		"schedule": strProp("When to run: a five-field cron expression, @hourly or @daily, every <duration>, or at <time>."),
		"prompt":   strProp("The instruction the scheduled turn runs."),
		"id":       strProp("Task id, for delete."),
		"name":     strProp("Short name for a durable job."),
		"durable":  boolProp("Store as a persistent cron job that survives restarts (runs under termixgo serve)."),
	})
}

func (t *scheduleTaskTool) Run(ctx context.Context, env *Env, args map[string]any) (Result, error) {
	scheduler := env.Schedules
	durable := argBool(args, "durable", false)
	switch strings.ToLower(strings.TrimSpace(argString(args, "action"))) {
	case "list":
		if scheduler == nil {
			return Result{Output: "Session scheduling is not available here."}, nil
		}
		return Result{Output: scheduler.List()}, nil
	case "delete", "remove", "cancel":
		if scheduler == nil || !scheduler.Remove(argString(args, "id")) {
			return Result{Output: "No session task with that id. Durable jobs are removed with /cron.", IsError: true}, nil
		}
		return Result{Output: "Removed " + argString(args, "id") + "."}, nil
	}
	spec := argString(args, "schedule", "cron")
	prompt := argString(args, "prompt")
	if strings.TrimSpace(spec) == "" || strings.TrimSpace(prompt) == "" {
		return Result{Output: "schedule and prompt are required", IsError: true}, nil
	}
	if durable {
		if env.AddCronJob == nil {
			return Result{Output: "Durable jobs are not available in this session.", IsError: true}, nil
		}
		summary, err := env.AddCronJob(argString(args, "name"), normalizeScheduleSpec(spec), prompt)
		if err != nil {
			return Result{Output: err.Error(), IsError: true}, nil
		}
		return Result{Output: summary + "\nIt runs while `termixgo serve` is running; manage it with /cron."}, nil
	}
	if scheduler == nil {
		return Result{Output: "Session scheduling is not available here; pass durable to store a cron job.", IsError: true}, nil
	}
	id, next, err := scheduler.Add(spec, prompt)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	return Result{Output: fmt.Sprintf("Scheduled %s, first run %s. It lasts for this session only; delete it with action delete.", id, next.Format("2006-01-02 15:04"))}, nil
}

// normalizeScheduleSpec accepts the @every and @at spellings models often use
// for the parser's every and at forms.
func normalizeScheduleSpec(spec string) string {
	trimmed := strings.TrimSpace(spec)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "@every ") || strings.HasPrefix(lower, "@at ") {
		return trimmed[1:]
	}
	return trimmed
}
