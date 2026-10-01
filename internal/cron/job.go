package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
)

// Job is one scheduled turn.
type Job struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Schedule Schedule `json:"schedule"`
	Prompt   string   `json:"prompt"`
	Enabled  bool     `json:"enabled"`

	// NextRun is persisted so a restart resumes rather than replaying. It is
	// advanced before the job runs, which makes execution at-most-once.
	NextRun time.Time `json:"nextRun,omitempty"`
	LastRun time.Time `json:"lastRun,omitempty"`
	// LastError is the last failure, kept so `cron list` can show why a job
	// stopped producing output.
	LastError string `json:"lastError,omitempty"`
}

// DefaultPath is where jobs live: ~/.termixgo/cron/jobs.json.
func DefaultPath() (string, error) {
	return config.HomePath(filepath.Join("cron", "jobs.json"))
}

// Store owns the job list and keeps it on disk. Every mutation is written
// through, so a crash loses at most the run in flight.
type Store struct {
	mu   sync.Mutex
	path string
	jobs []Job
}

// Open loads the jobs at path. A missing file is an empty store, not an error.
func Open(path string) (*Store, error) {
	store := &Store{path: path}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.jobs = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("read cron jobs %s: %w", s.path, err)
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return fmt.Errorf("parse cron jobs %s: %w", s.path, err)
	}
	s.jobs = jobs
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create cron directory: %w", err)
	}
	data, err := json.MarshalIndent(s.jobs, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cron jobs: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write cron jobs: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace cron jobs: %w", err)
	}
	return nil
}

// Path reports where the jobs are stored.
func (s *Store) Path() string { return s.path }

// List returns a copy of the jobs.
func (s *Store) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, len(s.jobs))
	copy(out, s.jobs)
	return out
}

// Add creates a job, computing its first run. It refuses a one-shot already in
// the past, which would otherwise be due immediately and never again.
func (s *Store) Add(name, prompt string, schedule Schedule, now time.Time) (Job, error) {
	if !schedule.Valid() {
		return Job{}, fmt.Errorf("the schedule is not valid")
	}
	if strings.TrimSpace(prompt) == "" {
		return Job{}, fmt.Errorf("the prompt is empty")
	}
	next, ok := schedule.Next(now)
	if !ok {
		return Job{}, fmt.Errorf("the schedule has no future run")
	}
	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	job := Job{
		ID:       id,
		Name:     strings.TrimSpace(name),
		Schedule: schedule,
		Prompt:   strings.TrimSpace(prompt),
		Enabled:  true,
		NextRun:  next,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
	if err := s.saveLocked(); err != nil {
		s.jobs = s.jobs[:len(s.jobs)-1]
		return Job{}, err
	}
	return job, nil
}

// Get returns one job by id.
func (s *Store) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range s.jobs {
		if job.ID == id {
			return job, true
		}
	}
	return Job{}, false
}

// Reload re-reads the jobs from disk, so an edit made by another process (the
// CLI while `serve` runs) is picked up on the next tick.
func (s *Store) Reload() error {
	return s.load()
}

// Remove deletes a job by id, reporting whether one matched.
func (s *Store) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, job := range s.jobs {
		if job.ID != id {
			continue
		}
		s.jobs = append(s.jobs[:index], s.jobs[index+1:]...)
		if err := s.saveLocked(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// SetEnabled toggles a job. Enabling recomputes the next run from now, so a
// long-disabled job does not fire a burst of missed runs.
func (s *Store) SetEnabled(id string, enabled bool, now time.Time) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.jobs {
		if s.jobs[index].ID != id {
			continue
		}
		s.jobs[index].Enabled = enabled
		if enabled {
			next, ok := s.jobs[index].Schedule.Next(now)
			if !ok {
				return Job{}, false, fmt.Errorf("the schedule has no future run")
			}
			s.jobs[index].NextRun = next
		}
		if err := s.saveLocked(); err != nil {
			return Job{}, false, err
		}
		return s.jobs[index], true, nil
	}
	return Job{}, false, nil
}

// Due returns enabled jobs whose next run is at or before now. A job with no
// next run set (a hand-edited file) is scheduled from now rather than fired.
func (s *Store) Due(now time.Time) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []Job
	changed := false
	for index := range s.jobs {
		job := &s.jobs[index]
		if !job.Enabled {
			continue
		}
		if job.NextRun.IsZero() {
			if next, ok := job.Schedule.Next(now); ok {
				job.NextRun = next
				changed = true
			}
			continue
		}
		if !job.NextRun.After(now) {
			due = append(due, *job)
		}
	}
	if changed {
		_ = s.saveLocked()
	}
	return due
}

// MarkStarted records that a job is running and advances its next run. A
// one-shot that has fired is disabled, because it has no next run.
func (s *Store) MarkStarted(id string, now time.Time) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.jobs {
		if s.jobs[index].ID != id {
			continue
		}
		s.jobs[index].LastRun = now
		next, ok := s.jobs[index].Schedule.Next(now)
		if ok {
			s.jobs[index].NextRun = next
		} else {
			s.jobs[index].NextRun = time.Time{}
			s.jobs[index].Enabled = false
		}
		if err := s.saveLocked(); err != nil {
			return Job{}, false, err
		}
		return s.jobs[index], true, nil
	}
	return Job{}, false, nil
}

// MarkError records a failure without touching the schedule.
func (s *Store) MarkError(id string, failure error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.jobs {
		if s.jobs[index].ID != id {
			continue
		}
		if failure == nil {
			s.jobs[index].LastError = ""
		} else {
			s.jobs[index].LastError = failure.Error()
		}
		return s.saveLocked()
	}
	return nil
}

func newID() (string, error) {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate a job id: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
