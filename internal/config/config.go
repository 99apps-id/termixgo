package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the non-secret settings file inside the state directory.
const FileName = "config.json"

// ApprovalMode decides when a tool call waits for the operator.
type ApprovalMode string

const (
	// ApprovalAsk waits for every write and every command.
	ApprovalAsk ApprovalMode = "ask"
	// ApprovalEdits runs file edits and waits for commands.
	ApprovalEdits ApprovalMode = "edits"
	// ApprovalAll runs everything without waiting. It is what makes a
	// trusted folder feel like a normal agent session.
	ApprovalAll ApprovalMode = "all"
	// ApprovalPlan blocks every mutating tool without asking. The model can
	// read, search and plan, which suits exploring a repository before any
	// change is allowed.
	ApprovalPlan ApprovalMode = "plan"
)

// ApprovalModes is the accepted set, in escalation order.
var ApprovalModes = []ApprovalMode{ApprovalAsk, ApprovalEdits, ApprovalAll, ApprovalPlan}

// ValidApprovalMode reports whether the mode is one this build accepts.
func ValidApprovalMode(mode ApprovalMode) bool {
	for _, known := range ApprovalModes {
		if known == mode {
			return true
		}
	}
	return false
}

// ParseApprovalMode normalises user input, accepting a unique prefix.
func ParseApprovalMode(raw string) (ApprovalMode, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	for _, mode := range ApprovalModes {
		if string(mode) == value {
			return mode, nil
		}
	}
	var matches []ApprovalMode
	for _, mode := range ApprovalModes {
		if strings.HasPrefix(string(mode), value) && value != "" {
			matches = append(matches, mode)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", fmt.Errorf("approval mode must be one of ask, edits, all, plan")
}

// Telegram is the companion bot pairing, minus the token, which is a secret.
type Telegram struct {
	Enabled     bool   `json:"enabled"`
	ChatID      int64  `json:"chatId,omitempty"`
	OwnerUserID int64  `json:"ownerUserId,omitempty"`
	PairingCode string `json:"pairingCode,omitempty"`
}

// Heartbeat runs a periodic isolated turn. The turn answers HEARTBEAT_OK when
// it has nothing worth saying, so a quiet assistant never pings.
type Heartbeat struct {
	Enabled bool `json:"enabled,omitempty"`
	// Interval is a Go duration such as 30m or 2h. Empty means 30m.
	Interval string `json:"interval,omitempty"`
}

// MCPServer configures one Model Context Protocol server for this install.
//
// A server that is listed is wanted by definition, so the flag is the
// exception rather than the rule: Disabled parks a server without deleting the
// command line, which is what an operator wants when a server is misbehaving
// and they intend to bring it back.
type MCPServer struct {
	// Name labels the server and prefixes every tool it contributes.
	Name string `json:"name"`
	// Command is the executable to run. The server speaks the protocol on its
	// stdio, so a command that prints a banner there will not work.
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Disabled keeps a server out of the session without removing it.
	Disabled bool `json:"disabled,omitempty"`
}

// Config is the whole non-secret configuration.
type Config struct {
	Version int `json:"version"`

	DefaultModel  string       `json:"defaultModel"`
	ApprovalMode  ApprovalMode `json:"approvalMode"`
	ShowReasoning bool         `json:"showReasoning"`
	Language      string       `json:"language"`
	// MaxSteps is the whole per-turn ceiling: the agent pauses after this many
	// steps in one reply, and the operator continues if the work is not done.
	// It is one explicit number, not a segment of a larger hidden budget.
	MaxSteps int `json:"maxSteps"`

	// CostBudgetUSD stops a run once the estimated spend for the session
	// passes it. Zero means no limit, which is the default so nothing
	// surprises an operator mid-task. The figure is an estimate from
	// published list prices, not a bill.
	CostBudgetUSD float64 `json:"costBudgetUsd,omitempty"`

	// SystemPrompt is appended to the built-in prompt when set.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// HarnessProfile selects the agent harness. Empty means critical.
	HarnessProfile string `json:"harnessProfile,omitempty"`

	// ToolSearch keeps the ecosystem tools (GitHub, pipelines, images, skills,
	// memory, web) out of every request and loads them on demand through
	// find_tools. It trades one discovery round trip for a smaller tool schema
	// on every step, which matters with a large toolset.
	ToolSearchEnabled bool `json:"toolSearchEnabled,omitempty"`

	// BaseURLs overrides provider endpoints, keyed by provider id. Local
	// providers (ollama, lmstudio) read it too.
	BaseURLs map[string]string `json:"baseUrls,omitempty"`

	// ModelOverrides maps a registry model id to the id sent on the wire,
	// which is how a renamed vendor model keeps a stable local id.
	ModelOverrides map[string]string `json:"modelOverrides,omitempty"`

	// ModelPricing supplies or overrides token prices per model id, so a
	// custom endpoint still gets a working cost budget.
	ModelPricing map[string]ModelPrice `json:"modelPricing,omitempty"`

	// WorkerCommands overrides the command line for a background coding
	// worker, keyed by worker id (termixgo, claude, codex, opencode). Each
	// entry is an argv; an element containing "{task}" is replaced by the task
	// text, and an entry with no placeholder gets the task appended. A worker
	// with no entry uses the built-in command.
	WorkerCommands map[string][]string `json:"workerCommands,omitempty"`

	// TrustedFolders is the canonical list of folders where the agent may
	// write and run commands without per-action approval.
	TrustedFolders []string `json:"trustedFolders,omitempty"`

	// AlwaysAllowedTools are tools the operator answered "allow always" for.
	AlwaysAllowedTools []string `json:"alwaysAllowedTools,omitempty"`

	// MCPServers are the Model Context Protocol servers to start with a
	// session. Each one contributes its tools to the agent, gated by the
	// approval policy because they run in processes this program did not write.
	MCPServers []MCPServer `json:"mcpServers,omitempty"`

	RecentProjects []string `json:"recentProjects,omitempty"`
	Telegram       Telegram `json:"telegram,omitempty"`

	// Heartbeat is the periodic self-check. Its jobs live in a separate file,
	// but whether it runs at all is configuration.
	Heartbeat Heartbeat `json:"heartbeat,omitempty"`
}

// Default returns the configuration a fresh install starts from.
func Default() Config {
	return Config{
		Version:       1,
		DefaultModel:  "",
		ApprovalMode:  ApprovalAll,
		ShowReasoning: true,
		Language:      "en",
		MaxSteps:      300,
		BaseURLs:      map[string]string{},
	}
}

// Path returns the settings file path.
func Path() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, FileName), nil
}

// Load reads the settings, falling back to defaults when the file is absent.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.normalise()
	return cfg, nil
}

// Save writes the settings atomically with private permissions.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	cfg.Version = 1
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// normalise repairs values a hand-edited file could have broken.
func (c *Config) normalise() {
	if !ValidApprovalMode(c.ApprovalMode) {
		c.ApprovalMode = ApprovalAll
	}
	// A budget of zero or less means "unset", so it becomes the default. The
	// budget only bounds one turn: a long task continues when the operator
	// replies, and the loop guard and cost cap remain the real runaway stops.
	if c.MaxSteps <= 0 {
		c.MaxSteps = 300
	}
	if c.Language == "" {
		c.Language = "en"
	}
	if c.CostBudgetUSD < 0 {
		c.CostBudgetUSD = 0
	}
	if c.BaseURLs == nil {
		c.BaseURLs = map[string]string{}
	}
	if strings.TrimSpace(c.Heartbeat.Interval) == "" {
		c.Heartbeat.Interval = "30m"
	}
	// A server with no command would start a process that cannot exist, so it
	// is dropped rather than left to fail on every session start.
	configured := make([]MCPServer, 0, len(c.MCPServers))
	seen := map[string]bool{}
	for _, server := range c.MCPServers {
		server.Name = strings.TrimSpace(server.Name)
		server.Command = strings.TrimSpace(server.Command)
		if server.Name == "" || server.Command == "" {
			continue
		}
		// Names have to be unique: the name is the prefix of every tool the
		// server contributes, so two servers sharing one would make the second
		// server's tools collide with the first's.
		if seen[server.Name] {
			continue
		}
		seen[server.Name] = true
		configured = append(configured, server)
	}
	c.MCPServers = configured
}

// BaseURL returns the configured endpoint for a provider, or the fallback.
func (c Config) BaseURL(provider, fallback string) string {
	if value := strings.TrimSpace(c.BaseURLs[provider]); value != "" {
		return value
	}
	return fallback
}

// WithRecent returns a copy whose recent-project list has path first, capped.
func (c Config) WithRecent(path string) Config {
	if strings.TrimSpace(path) == "" {
		return c
	}
	next := []string{path}
	for _, existing := range c.RecentProjects {
		if !samePath(existing, path) {
			next = append(next, existing)
		}
		if len(next) == 10 {
			break
		}
	}
	c.RecentProjects = next
	return c
}
