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
)

// ApprovalModes is the accepted set, in escalation order.
var ApprovalModes = []ApprovalMode{ApprovalAsk, ApprovalEdits, ApprovalAll}

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
	return "", fmt.Errorf("approval mode must be one of ask, edits, all")
}

// Telegram is the companion bot pairing, minus the token, which is a secret.
type Telegram struct {
	Enabled     bool   `json:"enabled"`
	ChatID      int64  `json:"chatId,omitempty"`
	OwnerUserID int64  `json:"ownerUserId,omitempty"`
	PairingCode string `json:"pairingCode,omitempty"`
}

// Config is the whole non-secret configuration.
type Config struct {
	Version int `json:"version"`

	DefaultModel  string       `json:"defaultModel"`
	ApprovalMode  ApprovalMode `json:"approvalMode"`
	ShowReasoning bool         `json:"showReasoning"`
	Language      string       `json:"language"`
	MaxSteps      int          `json:"maxSteps"`

	// CostBudgetUSD stops a run once the estimated spend for the session
	// passes it. Zero means no limit, which is the default so nothing
	// surprises an operator mid-task. The figure is an estimate from
	// published list prices, not a bill.
	CostBudgetUSD float64 `json:"costBudgetUsd,omitempty"`

	// SystemPrompt is appended to the built-in prompt when set.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// BaseURLs overrides provider endpoints, keyed by provider id. Local
	// providers (ollama, lmstudio) read it too.
	BaseURLs map[string]string `json:"baseUrls,omitempty"`

	// ModelOverrides maps a registry model id to the id sent on the wire,
	// which is how a renamed vendor model keeps a stable local id.
	ModelOverrides map[string]string `json:"modelOverrides,omitempty"`

	// ModelPricing supplies or overrides token prices per model id, so a
	// custom endpoint still gets a working cost budget.
	ModelPricing map[string]ModelPrice `json:"modelPricing,omitempty"`

	// TrustedFolders is the canonical list of folders where the agent may
	// write and run commands without per-action approval.
	TrustedFolders []string `json:"trustedFolders,omitempty"`

	// AlwaysAllowedTools are tools the operator answered "allow always" for.
	AlwaysAllowedTools []string `json:"alwaysAllowedTools,omitempty"`

	RecentProjects []string `json:"recentProjects,omitempty"`
	Telegram       Telegram `json:"telegram,omitempty"`
}

// Default returns the configuration a fresh install starts from.
func Default() Config {
	return Config{
		Version:       1,
		DefaultModel:  "",
		ApprovalMode:  ApprovalAll,
		ShowReasoning: true,
		Language:      "en",
		MaxSteps:      25,
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
	if c.MaxSteps <= 0 {
		c.MaxSteps = 25
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
