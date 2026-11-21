package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
)

// Permissions lists the tool allowances in force: the ones kept in the config
// ("always"), the ones granted for the current folder, and the ones allowed
// for this session only.
type Permissions struct {
	Always  []string
	Folder  []string
	Session []string
}

// Permissions reports the current allowances, each list sorted.
func (a *App) Permissions() Permissions {
	a.mu.Lock()
	always := append([]string(nil), a.cfg.AlwaysAllowedTools...)
	folder := a.cfg.ToolsAllowedInFolder(a.workspace)
	var session []string
	if a.policy != nil {
		for name, ok := range a.policy.SessionAllowed {
			if ok && !containsString(always, name) {
				session = append(session, name)
			}
		}
	}
	for name, ok := range a.folderGranted {
		if ok && !containsString(folder, name) && !containsString(session, name) {
			session = append(session, name)
		}
	}
	a.mu.Unlock()
	sort.Strings(always)
	sort.Strings(folder)
	sort.Strings(session)
	return Permissions{Always: always, Folder: folder, Session: session}
}

// RevokeTool removes every allowance for one tool: always, this folder's
// grant and this session's, so its next call asks again.
func (a *App) RevokeTool(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name the tool to revoke")
	}
	a.mu.Lock()
	if a.policy != nil {
		delete(a.policy.AlwaysAllowed, name)
		delete(a.policy.SessionAllowed, name)
	}
	delete(a.folderGranted, name)
	workspace := a.workspace
	a.mu.Unlock()
	return a.UpdateConfig(func(c *config.Config) {
		c.AlwaysAllowedTools = withoutString(c.AlwaysAllowedTools, name)
		key := config.TrustKey(workspace)
		for folder, tools := range c.FolderAllowedTools {
			if config.TrustKey(folder) == key {
				c.FolderAllowedTools[folder] = withoutString(tools, name)
				if len(c.FolderAllowedTools[folder]) == 0 {
					delete(c.FolderAllowedTools, folder)
				}
			}
		}
	})
}

// RevokeAllTools drops every allowance at once.
func (a *App) RevokeAllTools() error {
	for _, name := range append(append(a.Permissions().Always, a.Permissions().Folder...), a.Permissions().Session...) {
		if err := a.RevokeTool(name); err != nil {
			return err
		}
	}
	return nil
}

func withoutString(values []string, drop string) []string {
	out := values[:0:0]
	for _, value := range values {
		if value != drop {
			out = append(out, value)
		}
	}
	return out
}
