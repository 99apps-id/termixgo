package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
)

// TestDiscoverSurvivesAnUnresolvableHome is the defensive case: with no state
// directory to read, project skills must still load. A broken profile must not
// make every skill in the repository invisible.
func TestDiscoverSurvivesAnUnresolvableHome(t *testing.T) {
	t.Setenv(config.EnvHome, "")
	// Both names are cleared because the platform picks one of them, and an
	// empty value is what makes os.UserHomeDir fail.
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOME", "")

	if got := UserDir(); got != "" {
		t.Errorf("UserDir = %q, want empty when the home directory cannot be resolved", got)
	}

	workspace := t.TempDir()
	writeSkill(t, filepath.Join(workspace, ".termixgo", "skills", "local"), "local", "in the repo")
	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "local" {
		t.Fatalf("skills = %+v, want the project skill", skills)
	}
	if skills[0].Scope != "project" {
		t.Errorf("scope = %q", skills[0].Scope)
	}
}

// TestDiscoverSkipsEntriesWithoutADocument covers the junk that accumulates in
// a skills folder: a stray file, an empty folder, and a folder whose SKILL.md
// is missing.
func TestDiscoverSkipsEntriesWithoutADocument(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	root := filepath.Join(workspace, ".termixgo", "skills")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "no-doc", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(root, "real"), "real", "a real skill")

	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "real" {
		t.Fatalf("skills = %+v, want only the real one", skills)
	}
}

// TestDiscoverFallsBackToTheFolderName keeps a skill usable when its
// frontmatter omits the name, which is easy to forget.
func TestDiscoverFallsBackToTheFolderName(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".termixgo", "skills", "folder-name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No frontmatter at all, so the body is the whole document.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Do the thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("skills = %+v", skills)
	}
	if skills[0].Name != "folder-name" {
		t.Errorf("name = %q, want the folder name", skills[0].Name)
	}
	if skills[0].Description != "" {
		t.Errorf("description = %q, want empty", skills[0].Description)
	}
	if !strings.Contains(skills[0].Body, "Do the thing") {
		t.Errorf("body = %q, want the document", skills[0].Body)
	}
}

// TestDiscoverReportsBrokenFrontmatter pins that a typo in one skill is
// reported rather than silently dropping the skill.
func TestDiscoverReportsBrokenFrontmatter(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".termixgo", "skills", "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: [unclosed\ndescription: x\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Discover(workspace); err == nil {
		t.Fatalf("a malformed frontmatter must be reported")
	}
	// The direct parser has to agree, so the two cannot drift.
	if _, _, err := Parse([]byte(document)); err == nil {
		t.Errorf("Parse should reject the same document")
	}
}

func TestHelperFilesExcludesTheDocumentAndFolders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checklist.md", "example.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}

	files := helperFiles(dir)
	if len(files) != 2 {
		t.Fatalf("files = %v, want the two helper files", files)
	}
	// Sorted, so the model always sees the same order.
	if files[0] != "checklist.md" || files[1] != "example.go" {
		t.Errorf("files = %v, want them sorted", files)
	}
	if helperFiles(filepath.Join(dir, "missing")) != nil {
		t.Errorf("an unreadable folder should list nothing")
	}
}

// TestDiscoveredSkillCarriesItsHelperFiles ties the two together, because the
// model is only told about the files through the discovered skill.
func TestDiscoveredSkillCarriesItsHelperFiles(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".termixgo", "skills", "with-files")
	writeSkill(t, dir, "with-files", "ships a checklist")
	if err := os.WriteFile(filepath.Join(dir, "checklist.md"), []byte("steps"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 1 || len(skills[0].Files) != 1 || skills[0].Files[0] != "checklist.md" {
		t.Fatalf("skills = %+v, want the helper file listed", skills)
	}
	if skills[0].Path != dir {
		t.Errorf("path = %q, want %q", skills[0].Path, dir)
	}
}

func TestCreateRejectsAnImpossibleName(t *testing.T) {
	workspace := t.TempDir()
	if _, err := Create("  ", "x", "y"); err == nil {
		t.Errorf("a blank workspace must be rejected")
	}
	if _, err := Create(workspace, "!!!", "y"); err == nil {
		t.Errorf("a name with nothing usable in it must be rejected")
	}
}

func TestCreateSuppliesADefaultDescription(t *testing.T) {
	// Discover reads the user skills folder as well as the project one, so
	// without a private state directory the count below is whatever the
	// machine running the test happens to have installed. Every other
	// Discover test isolates the home for the same reason.
	t.Setenv(config.EnvHome, t.TempDir())
	workspace := t.TempDir()
	created, err := Create(workspace, "no description", "   ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if strings.TrimSpace(created.Description) == "" {
		t.Errorf("an empty description should be replaced, got %q", created.Description)
	}
	// The description is what the system prompt shows, so it has to reach the
	// discovered skill too.
	skills, err := Discover(workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 1 || skills[0].Description != created.Description {
		t.Errorf("skills = %+v, want the created description", skills)
	}
}

func TestLoadReportsAMissingSkill(t *testing.T) {
	workspace := t.TempDir()
	if _, err := Load(workspace, "not-there"); err == nil {
		t.Fatalf("loading a skill that does not exist must fail")
	}
	if _, err := Load(workspace, "  "); err == nil {
		t.Fatalf("loading a blank name must fail")
	}
}

func TestParseIgnoresAnUnterminatedFrontmatter(t *testing.T) {
	// A document that opens with the delimiter and never closes it is not
	// frontmatter; treating it as such would swallow the whole body.
	document := "---\nname: half a header\n"
	frontmatter, body, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if frontmatter.Name != "" {
		t.Errorf("frontmatter = %+v, want none", frontmatter)
	}
	if body != document {
		t.Errorf("body = %q, want the document unchanged", body)
	}
}

func TestPromptBlockSaysNoDescription(t *testing.T) {
	block := PromptBlock([]Skill{{Name: "bare"}})
	if !strings.Contains(block, "- bare: (no description)") {
		t.Errorf("block = %q, want the placeholder", block)
	}
}

func TestNormalizeNameKeepsDigitsAndSeparators(t *testing.T) {
	cases := map[string]string{
		"sql-migration":  "sql-migration",
		"SQL_Migration2": "sql_migration2",
		"a  b":           "a--b",
		"-trimmed-":      "trimmed",
	}
	for input, want := range cases {
		if got := NormalizeName(input); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
}
