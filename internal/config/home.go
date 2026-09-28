// Package config owns the user-level Termixgo state that is not secret:
// the model choice, approval policy, trust list, provider endpoints and the
// Telegram pairing. Secrets (provider keys, the bot token) live in the
// secrets store instead, never here.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvHome overrides the state directory, which is what keeps tests and
// throwaway profiles off the real one.
const EnvHome = "TERMIXGO_HOME"

// Home returns the Termixgo state directory, creating nothing.
func Home() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvHome)); override != "" {
		return override, nil
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(base, ".termixgo"), nil
}

// EnsureHome returns the state directory, creating it with private
// permissions when missing.
func EnsureHome() (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	return home, nil
}

// HomePath returns the absolute path of a file inside the state directory.
func HomePath(name string) (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, name), nil
}

// SessionsDir is where saved conversations live.
func SessionsDir() (string, error) {
	home, err := EnsureHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create sessions directory: %w", err)
	}
	return dir, nil
}
