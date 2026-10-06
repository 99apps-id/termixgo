package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

func TestParseSplitsFrontmatter(t *testing.T) {
	document := "---\nname: review\ndescription: Review a change set.\n---\n\n# Review\n\nSteps here.\n"
	frontmatter, body, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if frontmatter.Name != "review" {
		t.Errorf("Name = %q, want review", frontmatter.Name)
	}
	if frontmatter.Description != "Review a change set." {
		t.Errorf("Description = %q", frontmatter.Description)
	}
	if body != "\n# Review\n\nSteps here.\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseWithoutFrontmatterReturnsBody(t *testing.T) {
	frontmatter, body, err := Parse([]byte("# Just markdown\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if frontmatter.Name != "" {
		t.Errorf("expected no frontmatter, got %+v", frontmatter)
	}
	if body != "# Just markdown\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseHandlesCRLF(t *testing.T) {
	document := "---\r\nname: a\r\ndescription: b\r\n---\r\nbody\r\n"
	frontmatter, body, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if frontmatter.Name != "a" || frontmatter.Description != "b" {
		t.Errorf("frontmatter = %+v", frontmatter)
	}
	if body != "body\n" {
		t.Errorf("body = %q", body)
	}
}

func TestDiscoverPrefersProjectScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	workspace := t.TempDir()

	writeSkill(t, filepath.Join(home, "skills", "shared"), "shared", "user scope")
	writeSkill(t, filepath.Join(workspace, ".termixgo", "skills", "shared"), "shared", "project scope")
	writeSkill(t, filepath.Join(workspace, ".termixgo", "skills", "only-project"), "only-project", "project only")

	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	byName := map[string]Skill{}
	for _, item := range skills {
		byName[item.Name] = item
	}
	if got := byName["shared"].Description; got != "project scope" {
		t.Errorf("project scope should win, got %q", got)
	}
	if got := byName["only-project"].Scope; got != "project" {
		t.Errorf("only-project scope = %q", got)
	}
	// Builtins ride along, so they join the two authored skills.
	if len(skills) != 4 {
		t.Errorf("expected two authored skills plus two builtins, got %d", len(skills))
	}
	for _, want := range []string{"hallmark", "impeccable"} {
		skill, ok := byName[want]
		if !ok {
			t.Errorf("builtin %q is missing", want)
			continue
		}
		if skill.Scope != "builtin" {
			t.Errorf("builtin %q scope = %q, want builtin", want, skill.Scope)
		}
		if strings.TrimSpace(skill.Body) == "" {
			t.Errorf("builtin %q has no body", want)
		}
	}
}

// TestBuiltinSkillIsShadowedByProject pins the override rule: an operator
// skill with the same name wins over the compiled default, so an explicit
// choice is never trapped by the builtin.
func TestBuiltinSkillIsShadowedByProject(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	writeSkill(t, filepath.Join(workspace, ".termixgo", "skills", "hallmark"), "hallmark", "our house style")

	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	count := 0
	for _, item := range skills {
		if item.Name == "hallmark" {
			count++
			if item.Scope != "project" || item.Description != "our house style" {
				t.Errorf("project skill should win, got %+v", item)
			}
		}
	}
	if count != 1 {
		t.Errorf("hallmark appears %d times, want exactly the project one", count)
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Code Review":  "code-review",
		"  Weird__ID ": "weird__id",
		"!!!":          "",
	}
	for input, want := range cases {
		if got := NormalizeName(input); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCreateRejectsDuplicate(t *testing.T) {
	workspace := t.TempDir()
	if _, err := Create(workspace, "my skill", "Does things."); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Create(workspace, "My Skill", "Again."); err == nil {
		t.Fatalf("creating the same skill twice must fail")
	}
	loaded, err := Load(workspace, "MY-SKILL")
	if err != nil {
		t.Fatalf("Load should be case-insensitive, got %v", err)
	}
	if loaded.Description != "Does things." {
		t.Errorf("description = %q", loaded.Description)
	}
}

func TestPromptBlockListsDescriptions(t *testing.T) {
	block := PromptBlock([]Skill{{Name: "review", Description: "Review."}})
	if block == "" {
		t.Fatal("expected a block")
	}
	for _, want := range []string{"## SKILLS", "- review: Review."} {
		if !strings.Contains(block, want) {
			t.Errorf("prompt block is missing %q:\n%s", want, block)
		}
	}
	if PromptBlock(nil) != "" {
		t.Errorf("an empty skill list should render nothing")
	}
}

func writeSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
