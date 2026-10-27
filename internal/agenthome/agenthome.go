// Package agenthome owns the on-disk home of a named agent: the persona and
// operator files, one workspace, one sessions directory and the per-agent
// memory and skills folders.
//
// The layout follows the reference agents (hermes profiles and openclaw
// workspaces): SOUL.md and USER.md at the root of the home, everything the
// agent reads or writes underneath, so two agents never share a file.
package agenthome

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/99apps-id/termixgo/internal/config"
)

//go:embed templates/*.md
var templateFS embed.FS

// Persona and operator file names inside an agent home.
const (
	SoulFile = "SOUL.md"
	UserFile = "USER.md"
)

// Templates lists the persona templates `termixgo agent add --template`
// accepts. The first is the default.
var Templates = []string{"assistant", "coding", "sales", "support", "superadmin"}

// Paths is the resolved layout of one agent home.
type Paths struct {
	Name      string
	Home      string
	Workspace string
	Sessions  string
	Memories  string
	Skills    string
	Soul      string
	User      string
}

// ValidTemplate reports whether a persona template name exists.
func ValidTemplate(name string) bool {
	for _, candidate := range Templates {
		if candidate == name {
			return true
		}
	}
	return false
}

// Resolve computes the layout without touching the disk. workspaceOverride is
// the workspace configured for the agent; empty means the workspace directory
// inside the agent home.
func Resolve(name, workspaceOverride string) (Paths, error) {
	if !config.ValidAgentName(name) {
		return Paths{}, fmt.Errorf("invalid agent name %q: use lowercase letters, digits, underscore or dash (max 32)", name)
	}
	home, err := config.HomePath(filepath.Join("agents", name))
	if err != nil {
		return Paths{}, err
	}
	workspace := strings.TrimSpace(workspaceOverride)
	if workspace == "" {
		workspace = filepath.Join(home, "workspace")
	} else {
		workspace = config.CleanFolder(workspace)
	}
	return Paths{
		Name:      name,
		Home:      home,
		Workspace: workspace,
		Sessions:  filepath.Join(home, "sessions"),
		Memories:  filepath.Join(home, "memories"),
		Skills:    filepath.Join(home, "skills"),
		Soul:      filepath.Join(home, SoulFile),
		User:      filepath.Join(home, UserFile),
	}, nil
}

// Ensure creates the agent home and writes the persona files when missing.
// Existing files are never overwritten: the operator's edits are the point.
func Ensure(name, workspaceOverride, template string) (Paths, error) {
	paths, err := Resolve(name, workspaceOverride)
	if err != nil {
		return Paths{}, err
	}
	if strings.TrimSpace(template) == "" {
		template = Templates[0]
	}
	if !ValidTemplate(template) {
		return Paths{}, fmt.Errorf("unknown persona template %q; use one of %s", template, strings.Join(Templates, ", "))
	}
	for _, dir := range []string{paths.Home, paths.Workspace, paths.Sessions, paths.Memories, paths.Skills} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Paths{}, fmt.Errorf("create agent directory %s: %w", dir, err)
		}
	}
	soul, err := templateText(template)
	if err != nil {
		return Paths{}, err
	}
	user, err := templateText("user")
	if err != nil {
		return Paths{}, err
	}
	if err := writeIfMissing(paths.Soul, soul); err != nil {
		return Paths{}, err
	}
	if err := writeIfMissing(paths.User, user); err != nil {
		return Paths{}, err
	}
	return paths, nil
}

// SessionsDirFor is where one agent's conversations live. The default agent
// keeps the shared sessions directory so an existing install migrates nothing.
func SessionsDirFor(name string) (string, error) {
	if strings.TrimSpace(name) == "" || name == "default" {
		return config.SessionsDir()
	}
	paths, err := Resolve(name, "")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(paths.Sessions, 0o700); err != nil {
		return "", fmt.Errorf("create agent sessions directory: %w", err)
	}
	return paths.Sessions, nil
}

// MemoriesFile is the curated memory file for one agent.
func MemoriesFile(name string) (string, error) {
	paths, err := Resolve(name, "")
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.Memories, "MEMORY.md"), nil
}

func templateText(name string) (string, error) {
	data, err := templateFS.ReadFile("templates/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("persona template %q is missing", name)
	}
	return string(data), nil
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}
