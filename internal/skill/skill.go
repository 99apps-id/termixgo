// Package skill discovers Termixgo skills.
//
// A skill is a folder with a SKILL.md file: YAML frontmatter (name,
// description) followed by Markdown instructions. Project skills live in
// <workspace>/.termixgo/skills/<name>/SKILL.md, user skills in
// <termixgo-home>/skills/<name>/SKILL.md. Only the short description reaches
// the system prompt; the body loads on demand through the use_skill tool.
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/skill/builtin"
)

// Skill is one discovered skill.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Path        string   `json:"path"`
	Body        string   `json:"body,omitempty"`
	Files       []string `json:"files,omitempty"`
	Scope       string   `json:"scope"`
}

// Frontmatter is the YAML header of a SKILL.md.
type Frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

const delimiter = "---"

// ProjectDir is the skills root inside a workspace.
func ProjectDir(workspace string) string {
	return filepath.Join(workspace, ".termixgo", "skills")
}

// UserDir is the skills root inside the state directory.
func UserDir() string {
	home, err := config.Home()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "skills")
}

// Discover returns project, user and builtin skills, sorted by name. Missing
// folders are not an error; a project skill shadows a user skill of the same
// name, and both shadow a builtin one. Builtins are appended last so an
// operator-authored skill always wins a name collision.
func Discover(workspace string) ([]Skill, error) {
	seen := map[string]int{}
	skills := make([]Skill, 0, 16)
	for _, source := range []struct{ root, scope string }{
		{ProjectDir(workspace), "project"},
		{UserDir(), "user"},
	} {
		if strings.TrimSpace(source.root) == "" || source.root == ".termixgo/skills" {
			continue
		}
		found, err := fromFolder(source.root, source.scope)
		if err != nil {
			return nil, err
		}
		for _, item := range found {
			key := strings.ToLower(item.Name)
			if index, ok := seen[key]; ok {
				// Project scope wins: it was appended first.
				if skills[index].Scope == "project" {
					continue
				}
				skills[index] = item
				continue
			}
			seen[key] = len(skills)
			skills = append(skills, item)
		}
	}
	for _, name := range builtin.Names {
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		document, ok := builtin.Read(name)
		if !ok {
			continue
		}
		frontmatter, body, err := Parse([]byte(document))
		if err != nil {
			return nil, fmt.Errorf("builtin skill %s: %w", name, err)
		}
		label := strings.TrimSpace(frontmatter.Name)
		if label == "" {
			label = name
		}
		seen[key] = len(skills)
		skills = append(skills, Skill{
			Name:        label,
			Description: strings.TrimSpace(frontmatter.Description),
			Body:        body,
			Scope:       "builtin",
		})
	}
	sort.SliceStable(skills, func(i, j int) bool {
		return strings.ToLower(skills[i].Name) < strings.ToLower(skills[j].Name)
	})
	return skills, nil
}

// Load returns one skill by name, case-insensitively.
func Load(workspace, name string) (Skill, error) {
	skills, err := Discover(workspace)
	if err != nil {
		return Skill{}, err
	}
	for _, candidate := range skills {
		if strings.EqualFold(candidate.Name, name) {
			return candidate, nil
		}
	}
	return Skill{}, fmt.Errorf("skill %q not found; check /skills list for the available names", name)
}

// Create scaffolds a project skill.
func Create(workspace, name, description string) (Skill, error) {
	if strings.TrimSpace(workspace) == "" {
		return Skill{}, errors.New("a workspace is required to create a project skill")
	}
	normalised := NormalizeName(name)
	if normalised == "" {
		return Skill{}, errors.New("skill name must contain a letter or digit")
	}
	if strings.TrimSpace(description) == "" {
		description = "A Termixgo skill."
	}
	dir := filepath.Join(ProjectDir(workspace), normalised)
	document := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(document); err == nil {
		return Skill{}, fmt.Errorf("skill %q already exists at %s", normalised, document)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Skill{}, fmt.Errorf("create skill folder: %w", err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# %s\n\nWhen to use this skill, what to inspect, and the expected output.\n",
		normalised, description, normalised)
	if err := os.WriteFile(document, []byte(content), 0o644); err != nil {
		return Skill{}, fmt.Errorf("write SKILL.md: %w", err)
	}
	return Load(workspace, normalised)
}

// NormalizeName reduces a name to a lowercase, hyphenated identifier.
func NormalizeName(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		case r == ' ':
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

// Parse splits a SKILL.md into frontmatter and body.
func Parse(data []byte) (Frontmatter, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, delimiter+"\n") {
		return Frontmatter{}, text, nil
	}
	rest := text[len(delimiter)+1:]
	end := strings.Index(rest, "\n"+delimiter)
	if end < 0 {
		return Frontmatter{}, text, nil
	}
	header := rest[:end]
	body := strings.TrimPrefix(rest[end+1+len(delimiter):], "\n")
	var frontmatter Frontmatter
	if err := yaml.Unmarshal([]byte(header), &frontmatter); err != nil {
		return Frontmatter{}, body, fmt.Errorf("parse frontmatter: %w", err)
	}
	return frontmatter, body, nil
}

func fromFolder(root, scope string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skills folder %s: %w", root, err)
	}
	skills := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s/SKILL.md: %w", dir, err)
		}
		frontmatter, body, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s/SKILL.md: %w", dir, err)
		}
		name := strings.TrimSpace(frontmatter.Name)
		if name == "" {
			name = entry.Name()
		}
		skills = append(skills, Skill{
			Name:        name,
			Description: strings.TrimSpace(frontmatter.Description),
			Path:        dir,
			Body:        body,
			Files:       helperFiles(dir),
			Scope:       scope,
		})
	}
	return skills, nil
}

// helperFiles lists non-SKILL.md files a skill ships, which the model is told
// about so it knows to read them.
func helperFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "SKILL.md" {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	return files
}

// PromptBlock renders the one-line-per-skill block the system prompt carries.
func PromptBlock(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\n\n## SKILLS - .termixgo/skills/\n")
	builder.WriteString("Load a skill with use_skill before following it.\n")
	for _, item := range skills {
		description := item.Description
		if description == "" {
			description = "(no description)"
		}
		fmt.Fprintf(&builder, "- %s: %s\n", item.Name, description)
	}
	return builder.String()
}
