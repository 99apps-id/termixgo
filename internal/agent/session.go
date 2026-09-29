package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/provider"
	"github.com/99apps-id/termixgo/internal/secrets"
)

// Session is one conversation, persisted so /sessions and resume work.
//
// The fields are unexported and reached through methods because a session is
// written by the goroutine running a turn while the terminal keeps reading it:
// the status bar asks for token usage, the transcript for the message list.
// The race detector caught exactly that, and unexported fields are what stop it
// recurring, since an unlocked read no longer compiles.
type Session struct {
	mu sync.Mutex

	id        string
	title     string
	workspace string
	model     string
	createdAt time.Time
	updatedAt time.Time

	messages []provider.Message
	todos    []Todo
	usage    provider.Usage
	costUSD  float64
}

// sessionJSON is the persisted shape. It mirrors the fields so the on-disk
// format stays stable across refactors.
type sessionJSON struct {
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Workspace string             `json:"workspace"`
	Model     string             `json:"model"`
	CreatedAt time.Time          `json:"createdAt"`
	UpdatedAt time.Time          `json:"updatedAt"`
	Messages  []provider.Message `json:"messages"`
	Todos     []Todo             `json:"todos,omitempty"`
	Usage     provider.Usage     `json:"usage,omitempty"`
	CostUSD   float64            `json:"costUsd,omitempty"`
}

// NewSession starts an empty conversation with a random id.
func NewSession(workspace, model string) *Session {
	now := time.Now()
	return &Session{
		id:        newID(),
		workspace: workspace,
		model:     model,
		createdAt: now,
		updatedAt: now,
	}
}

func newID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

// ID is the session identifier.
func (s *Session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// Workspace is the folder the session was started in.
func (s *Session) Workspace() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspace
}

// Title is the short summary shown in a listing.
func (s *Session) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

// Model is the model id the session runs on.
func (s *Session) Model() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

// SetModel records the model a session is running on.
//
// It moves the timestamp like the other setters: a session whose model changed
// has changed, and the listing sorts by last activity.
func (s *Session) SetModel(model string) {
	s.mu.Lock()
	s.model = model
	s.updatedAt = time.Now()
	s.mu.Unlock()
}

// CreatedAt is when the session began.
func (s *Session) CreatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createdAt
}

// UpdatedAt is when the session last changed.
func (s *Session) UpdatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updatedAt
}

// Messages returns a snapshot of the conversation.
//
// The slice header is copied so a reader can range over it while a turn
// appends. The messages themselves are never mutated after being added, so
// sharing the elements is safe and avoids copying every tool result.
func (s *Session) Messages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return nil
	}
	snapshot := make([]provider.Message, len(s.messages))
	copy(snapshot, s.messages)
	return snapshot
}

// MessageCount reports how many messages the conversation holds.
func (s *Session) MessageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// Todos returns a snapshot of the plan stored with the session.
func (s *Session) Todos() []Todo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.todos) == 0 {
		return nil
	}
	snapshot := make([]Todo, len(s.todos))
	copy(snapshot, s.todos)
	return snapshot
}

// SetTodos records the plan alongside the conversation.
func (s *Session) SetTodos(todos []Todo) {
	s.mu.Lock()
	if len(todos) == 0 {
		s.todos = nil
	} else {
		s.todos = append([]Todo(nil), todos...)
	}
	s.updatedAt = time.Now()
	s.mu.Unlock()
}

// Usage is the token accounting for the session.
func (s *Session) Usage() provider.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

// AddUsage folds more token usage into the session.
func (s *Session) AddUsage(usage provider.Usage) {
	s.mu.Lock()
	s.usage = s.usage.Add(usage)
	s.mu.Unlock()
}

// Cost is the estimated spend across the session.
func (s *Session) Cost() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.costUSD
}

// SetCost records the estimated spend, which a resumed session keeps counting
// against its budget.
func (s *Session) SetCost(cost float64) {
	s.mu.Lock()
	s.costUSD = cost
	s.updatedAt = time.Now()
	s.mu.Unlock()
}

// AddUser appends a user turn.
func (s *Session) AddUser(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, provider.Message{Role: provider.RoleUser, Content: text})
	if strings.TrimSpace(s.title) == "" {
		s.title = Shorten(text, 60)
	}
	s.updatedAt = time.Now()
}

// AddAssistant appends a completed assistant turn.
func (s *Session) AddAssistant(text, reasoning string, calls []provider.ToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, provider.Message{
		Role:      provider.RoleAssistant,
		Content:   text,
		Reasoning: reasoning,
		ToolCalls: calls,
	})
	s.updatedAt = time.Now()
}

// AddToolResult appends a tool result.
func (s *Session) AddToolResult(id, name, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, provider.Message{
		Role:    provider.RoleTool,
		Content: output,
		ToolID:  id,
		Name:    name,
	})
	s.updatedAt = time.Now()
}

// AddImages appends a user turn carrying image attachments. Providers accept
// images on a user message but not on a tool message, so a tool that produced an
// image (read_image) follows its tool result with this call.
func (s *Session) AddImages(text string, images []provider.Image) {
	if len(images) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, provider.Message{
		Role:    provider.RoleUser,
		Content: text,
		Images:  images,
	})
	s.updatedAt = time.Now()
}

// Reset clears the conversation but keeps the identity.
func (s *Session) Reset() {
	s.mu.Lock()
	s.messages = nil
	s.todos = nil
	s.usage = provider.Usage{}
	s.costUSD = 0
	s.title = ""
	s.updatedAt = time.Now()
	s.mu.Unlock()
}

// Turns counts user messages.
func (s *Session) Turns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, message := range s.messages {
		if message.Role == provider.RoleUser {
			count++
		}
	}
	return count
}

// LastAssistantText returns the newest non-empty assistant message, which is
// the answer a caller reports after a turn.
func (s *Session) LastAssistantText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.messages) - 1; index >= 0; index-- {
		message := s.messages[index]
		if message.Role == provider.RoleAssistant && strings.TrimSpace(message.Content) != "" {
			return message.Content
		}
	}
	return ""
}

// snapshot copies the state for persistence, taken under the lock.
func (s *Session) snapshot() sessionJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sessionJSON{
		ID:        s.id,
		Title:     s.title,
		Workspace: s.workspace,
		Model:     s.model,
		CreatedAt: s.createdAt,
		UpdatedAt: s.updatedAt,
		Messages:  s.messages,
		Todos:     s.todos,
		Usage:     s.usage,
		CostUSD:   s.costUSD,
	}
}

// Save writes the session to the state directory.
func (s *Session) Save() error {
	dir, err := config.SessionsDir()
	if err != nil {
		return err
	}
	// The snapshot is taken under the lock and marshalled outside it: holding
	// the lock through a disk write would block the run for no reason.
	state := s.snapshot()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	path := filepath.Join(dir, state.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	// The mode above only counts on POSIX. On Windows a plain write inherits
	// the parent directory's grants, and a conversation can contain a key the
	// operator pasted into it, so the file is restricted explicitly.
	if err := secrets.RestrictFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("protect session: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace session: %w", err)
	}
	// A rename can drop a freshly applied ACL on some filesystems.
	return secrets.RestrictFile(path)
}

// LoadSession reads one session by id.
func LoadSession(id string) (*Session, error) {
	dir, err := config.SessionsDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}
	var state sessionJSON
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	return fromJSON(state), nil
}

// fromJSON builds a live session from persisted state.
func fromJSON(state sessionJSON) *Session {
	created := state.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	updated := state.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	return &Session{
		id:        state.ID,
		title:     state.Title,
		workspace: state.Workspace,
		model:     state.Model,
		createdAt: created,
		updatedAt: updated,
		messages:  state.Messages,
		todos:     state.Todos,
		usage:     state.Usage,
		costUSD:   state.CostUSD,
	}
}

// SessionSummary is the listing shown by /sessions.
type SessionSummary struct {
	ID        string
	Title     string
	Model     string
	UpdatedAt time.Time
	Turns     int
}

// ListSessions returns saved sessions, newest first.
func ListSessions() ([]SessionSummary, error) {
	dir, err := config.SessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	summaries := make([]SessionSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var state sessionJSON
		if json.Unmarshal(data, &state) != nil {
			continue
		}
		session := fromJSON(state)
		summaries = append(summaries, SessionSummary{
			ID:        session.ID(),
			Title:     session.Title(),
			Model:     session.Model(),
			UpdatedAt: session.UpdatedAt(),
			Turns:     session.Turns(),
		})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt) })
	return summaries, nil
}
