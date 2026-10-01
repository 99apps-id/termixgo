package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/config"
	"github.com/99apps-id/termixgo/internal/skill"
)

// TestInstallSkillIsSubjectToApproval pins the classification: install_skill
// copies files onto disk, so it is a mutating tool. Reporting it as read-only
// let it run unnoticed in plan mode and in an untrusted folder.
func TestInstallSkillIsSubjectToApproval(t *testing.T) {
	tool := &installSkillTool{}
	if !tool.Mutating() {
		t.Fatalf("install_skill writes files, so Mutating must be true")
	}
	if tool.Risk() != RiskEdit {
		t.Errorf("risk = %q, want edit", tool.Risk())
	}
	// Plan mode blocks every mutating tool without asking, which is the
	// guarantee that exploring a repository changes nothing.
	plan := &ApprovalPolicy{Mode: ApprovalPlan}
	if !plan.NeedsApproval(tool) {
		t.Errorf("plan mode must gate install_skill")
	}
}

// skillFixture writes two skills into the workspace and returns the loaded
// list, so the skill tools are exercised against a real folder rather than a
// hand-built slice.
func skillFixture(t *testing.T, env *Env) []skill.Skill {
	t.Helper()
	specs := []struct{ name, description, body string }{
		{"release-notes", "Draft release notes from the commit log.", "Read git log, group by area, write the section."},
		{"sql-migration", "Write a reversible database migration.", "Always add the down migration."},
	}
	for _, spec := range specs {
		if _, err := skill.Create(env.Workspace, spec.name, spec.description); err != nil {
			t.Fatalf("create skill %s: %v", spec.name, err)
		}
		// Create scaffolds a template; replace the body so the test can assert
		// on text it controls.
		document := filepath.Join(skill.ProjectDir(env.Workspace), spec.name, "SKILL.md")
		content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", spec.name, spec.description, spec.body)
		if err := os.WriteFile(document, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", document, err)
		}
	}
	skills, err := skill.Discover(env.Workspace)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(skills))
	}
	return skills
}

func TestTodoReadReportsThePlan(t *testing.T) {
	env := testEnv(t)
	tool := &todoReadTool{}

	empty, err := tool.Run(context.Background(), env, nil)
	if err != nil || empty.IsError {
		t.Fatalf("an empty plan is not an error: err=%v result=%+v", err, empty)
	}
	if !strings.Contains(empty.Output, "empty") {
		t.Errorf("output = %q, want it to say the plan is empty", empty.Output)
	}

	if err := env.Todos.Write([]Todo{
		{ID: "1", Title: "read the code", Status: "completed"},
		{ID: "2", Title: "write the fix", Status: "in_progress"},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	plan, err := tool.Run(context.Background(), env, nil)
	if err != nil || plan.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, plan)
	}
	for _, want := range []string{"[completed] read the code", "[in_progress] write the fix"} {
		if !strings.Contains(plan.Output, want) {
			t.Errorf("output missing %q:\n%s", want, plan.Output)
		}
	}
}

func TestUseSkillNeedsANameAndFindsTheBody(t *testing.T) {
	env := testEnv(t)
	env.Skills = skillFixture(t, env)
	tool := &useSkillTool{}

	missing, err := tool.Run(context.Background(), env, map[string]any{})
	if err != nil || !missing.IsError {
		t.Fatalf("a missing name must be reported: err=%v result=%+v", err, missing)
	}

	unknown, err := tool.Run(context.Background(), env, map[string]any{"name": "nope"})
	if err != nil || !unknown.IsError {
		t.Fatalf("an unknown skill is an error: err=%v result=%+v", err, unknown)
	}
	// The error has to list what is available, or the model has nothing to
	// retry with.
	for _, want := range []string{"release-notes", "sql-migration"} {
		if !strings.Contains(unknown.Output, want) {
			t.Errorf("the refusal should list %q, got %q", want, unknown.Output)
		}
	}

	// The name match is case-insensitive, because the model may capitalise it.
	loaded, err := tool.Run(context.Background(), env, map[string]any{"name": "Release-Notes"})
	if err != nil || loaded.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, loaded)
	}
	if !strings.Contains(loaded.Output, "release-notes") || !strings.Contains(loaded.Output, "Read git log") {
		t.Errorf("output = %q, want the name and the body", loaded.Output)
	}
	if !strings.Contains(loaded.Output, "scope project") {
		t.Errorf("the scope should be stated, got %q", loaded.Output)
	}
}

func TestFindSkillSearchesNameAndDescription(t *testing.T) {
	env := testEnv(t)
	env.Skills = skillFixture(t, env)
	tool := &findSkillTool{}

	short, err := tool.Run(context.Background(), env, map[string]any{"query": "a"})
	if err != nil || !short.IsError {
		t.Fatalf("a one-character query must be refused: err=%v result=%+v", err, short)
	}

	// The match may be in the description rather than the name.
	found, err := tool.Run(context.Background(), env, map[string]any{"query": "migration"})
	if err != nil || found.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, found)
	}
	if !strings.Contains(found.Output, "sql-migration") {
		t.Errorf("output = %q, want the matching skill", found.Output)
	}

	none, err := tool.Run(context.Background(), env, map[string]any{"query": "kubernetes"})
	if err != nil || none.IsError {
		// No match is a normal answer, not a failure the model should retry.
		t.Fatalf("no match should not be an error: err=%v result=%+v", err, none)
	}
	if !strings.Contains(none.Output, "No skill matches") {
		t.Errorf("output = %q", none.Output)
	}
}

func TestSkillNamesSaysNoneWhenEmpty(t *testing.T) {
	if got := skillNames(nil); got != "(none)" {
		t.Errorf("skillNames = %q, want (none)", got)
	}
}

func TestAskUserNeedsAQuestionAndAnOperator(t *testing.T) {
	tool := &askUserTool{}

	env := testEnv(t)
	missing, err := tool.Run(context.Background(), env, map[string]any{})
	if err != nil || !missing.IsError {
		t.Fatalf("a missing question must be reported: err=%v result=%+v", err, missing)
	}

	// A headless run has no operator, so the model must be told rather than
	// left waiting for an answer that can never come.
	headless, err := tool.Run(context.Background(), env, map[string]any{"question": "which port?"})
	if err != nil || !headless.IsError {
		t.Fatalf("no operator must be reported: err=%v result=%+v", err, headless)
	}
	if !strings.Contains(headless.Output, "not available") {
		t.Errorf("output = %q", headless.Output)
	}

	env.Ask = func(question string, options []string) (string, error) {
		if question != "which port?" {
			t.Errorf("question = %q", question)
		}
		if len(options) != 2 {
			t.Errorf("options = %v, want the two offered", options)
		}
		return "8080", nil
	}
	answered, err := tool.Run(context.Background(), env, map[string]any{
		"question": "which port?",
		"options":  []any{"3000", "8080"},
	})
	if err != nil || answered.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, answered)
	}
	if !strings.Contains(answered.Output, "8080") {
		t.Errorf("output = %q, want the answer", answered.Output)
	}

	env.Ask = func(string, []string) (string, error) { return "", fmt.Errorf("the operator went away") }
	failed, err := tool.Run(context.Background(), env, map[string]any{"question": "which port?"})
	if err != nil || !failed.IsError {
		t.Fatalf("a failed question must be reported to the model: err=%v result=%+v", err, failed)
	}
}

func TestThinkRecordsReasoningAsEvents(t *testing.T) {
	env := testEnv(t)
	var events []Event
	env.Emit = func(event Event) { events = append(events, event) }
	tool := &thinkTool{}

	missing, err := tool.Run(context.Background(), env, map[string]any{})
	if err != nil || !missing.IsError {
		t.Fatalf("missing thoughts must be reported: err=%v result=%+v", err, missing)
	}

	result, err := tool.Run(context.Background(), env, map[string]any{"thoughts": "check the tests first"})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want a thinking event and a reasoned event", len(events))
	}
	// The pair is what makes the UI close the live thinking block, so the
	// order matters.
	if events[0].Kind != EventThinking || events[1].Kind != EventReasoned {
		t.Errorf("events = %v, %v; want thinking then reasoned", events[0].Kind, events[1].Kind)
	}
	if events[0].Text != "check the tests first" {
		t.Errorf("thinking text = %q", events[0].Text)
	}

	// A tool that cannot emit must still succeed: it is a display nicety.
	silent := testEnv(t)
	if result, err := tool.Run(context.Background(), silent, map[string]any{"thoughts": "no emitter"}); err != nil || result.IsError {
		t.Errorf("Run without an emitter: err=%v result=%+v", err, result)
	}
}

func TestSubagentToolGuardsAndHandOff(t *testing.T) {
	tool := &subagentTool{}
	env := testEnv(t)

	missing, err := tool.Run(context.Background(), env, map[string]any{})
	if err != nil || !missing.IsError {
		t.Fatalf("a missing prompt must be reported: err=%v result=%+v", err, missing)
	}

	unavailable, err := tool.Run(context.Background(), env, map[string]any{"prompt": "look around"})
	if err != nil || !unavailable.IsError {
		t.Fatalf("no subagent support must be reported: err=%v result=%+v", err, unavailable)
	}

	// Nesting is capped so a subagent cannot spawn its way to an unbounded
	// recursion.
	deep := testEnv(t)
	deep.Depth = 3
	deep.RunSubagent = func(context.Context, string, string) (string, error) { return "unused", nil }
	capped, err := tool.Run(context.Background(), deep, map[string]any{"prompt": "look around"})
	if err != nil || !capped.IsError {
		t.Fatalf("the depth cap must be enforced: err=%v result=%+v", err, capped)
	}

	env.RunSubagent = func(ctx context.Context, subType, prompt string) (string, error) {
		if prompt != "summarise the parser" {
			t.Errorf("prompt = %q", prompt)
		}
		if subType != string(SubagentGeneral) {
			t.Errorf("subType = %q, want the general worker by default", subType)
		}
		return "The parser has three stages.", nil
	}
	report, err := tool.Run(context.Background(), env, map[string]any{"prompt": "summarise the parser"})
	if err != nil || report.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, report)
	}
	if !strings.Contains(report.Output, "three stages") {
		t.Errorf("output = %q, want the report", report.Output)
	}

	env.RunSubagent = func(context.Context, string, string) (string, error) { return "", fmt.Errorf("no client") }
	failed, err := tool.Run(context.Background(), env, map[string]any{"prompt": "look around"})
	if err != nil || !failed.IsError {
		t.Fatalf("a failed subagent must be reported: err=%v result=%+v", err, failed)
	}
}

func TestWebFetchRefusesAnythingButHttp(t *testing.T) {
	tool := &webFetchTool{}
	env := testEnv(t)

	for _, raw := range []string{"", "file:///etc/passwd", "ftp://example.com/x", "not a url", "javascript:alert(1)"} {
		result, err := tool.Run(context.Background(), env, map[string]any{"url": raw})
		if err != nil {
			t.Fatalf("Run(%q): %v", raw, err)
		}
		if !result.IsError {
			t.Errorf("Run(%q) should be refused, got %q", raw, result.Output)
		}
	}
}

// TestWebFetchRefusesMetadataAddresses is the security rule: the cloud
// metadata endpoint hands out credentials, and it is never the documentation
// the model meant to read.
func TestWebFetchRefusesMetadataAddresses(t *testing.T) {
	tool := &webFetchTool{}
	env := testEnv(t)

	for _, host := range []string{
		"169.254.169.254",
		"metadata.google.internal",
		"169.254.1.1",
		"[fe80::1]",
	} {
		result, err := tool.Run(context.Background(), env, map[string]any{"url": "http://" + host + "/latest/meta-data/"})
		if err != nil {
			t.Fatalf("Run(%s): %v", host, err)
		}
		if !result.IsError {
			t.Errorf("%s must be refused, got %q", host, result.Output)
		}
	}
	// The rule must not block ordinary hosts.
	if isBlockedHost("example.com") || isBlockedHost("127.0.0.1") {
		t.Errorf("ordinary hosts must not be blocked")
	}
}

func TestWebFetchStripsHTMLAndReportsStatus(t *testing.T) {
	tool := &webFetchTool{}
	env := testEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("User-Agent") == "" {
			t.Errorf("the fetch should identify itself")
		}
		switch request.URL.Path {
		case "/docs":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(writer, `<html><head><style>body{color:red}</style><script>var x=1</script></head>
				<body><h1>Guide</h1><p>Use &amp; enjoy.</p></body></html>`)
		case "/plain":
			writer.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(writer, "line one\nline two")
		default:
			writer.WriteHeader(http.StatusNotFound)
			fmt.Fprint(writer, "missing")
		}
	}))
	defer server.Close()

	page, err := tool.Run(context.Background(), env, map[string]any{"url": server.URL + "/docs"})
	if err != nil || page.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, page)
	}
	if !strings.Contains(page.Output, "Guide") || !strings.Contains(page.Output, "Use & enjoy.") {
		t.Errorf("output = %q, want the readable text", page.Output)
	}
	// Scripts and styles are not documentation, and leaving them in burns the
	// context window on noise.
	if strings.Contains(page.Output, "color:red") || strings.Contains(page.Output, "var x=1") {
		t.Errorf("output still contains script or style content:\n%s", page.Output)
	}
	if strings.Contains(page.Output, "<h1>") {
		t.Errorf("output still contains markup:\n%s", page.Output)
	}

	plain, err := tool.Run(context.Background(), env, map[string]any{"url": server.URL + "/plain"})
	if err != nil || plain.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, plain)
	}
	if plain.Output != "line one\nline two" {
		t.Errorf("output = %q, want the body untouched", plain.Output)
	}

	missing, err := tool.Run(context.Background(), env, map[string]any{"url": server.URL + "/nope"})
	if err != nil || !missing.IsError {
		t.Fatalf("a non-2xx status must be reported: err=%v result=%+v", err, missing)
	}
	if !strings.Contains(missing.Output, "404") {
		t.Errorf("output = %q, want the status", missing.Output)
	}
}

// TestHTMLToTextKeepsBlockBoundaries is the reason htmlToText splits on block
// tags before stripping them: a plain strip made <h1>Guide</h1><p>Use it</p>
// read as "GuideUse it", which merges words from different blocks.
func TestHTMLToTextKeepsBlockBoundaries(t *testing.T) {
	page := `<html><body>
		<h1>Guide</h1>
		<p>Use &amp; enjoy. It costs &#36;5 and a &mdash; no &copy; needed.</p>
		<ul><li>first</li><li>second</li></ul>
		<p>line<br>break</p>
	</body></html>`
	text := htmlToText(page)
	for _, want := range []string{"Guide", "Use & enjoy.", "first", "second", "line", "break"} {
		if !strings.Contains(text, want) {
			t.Errorf("text is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "GuideUse") {
		t.Errorf("adjacent blocks merged:\n%s", text)
	}
	if !strings.Contains(text, "$5") {
		t.Errorf("numeric entities should decode:\n%s", text)
	}
	if strings.Contains(text, "&mdash;") || strings.Contains(text, "&copy;") {
		t.Errorf("named entities should decode:\n%s", text)
	}
}

func TestWebFetchReportsAnUnreachableHost(t *testing.T) {
	tool := &webFetchTool{}
	env := testEnv(t)

	// A port nothing is listening on. 127.0.0.1 is not a blocked host, so the
	// failure comes from the connection rather than the guard.
	result, err := tool.Run(context.Background(), env, map[string]any{"url": "http://127.0.0.1:9/x"})
	if err != nil {
		t.Fatalf("a transport failure must be a tool result, not a Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("output = %q, want a failure", result.Output)
	}
	if !strings.Contains(result.Output, "fetch failed") {
		t.Errorf("output = %q, want it to name the failure", result.Output)
	}
}

// TestWebFetchReaderUsesTheReaderHost proves reader=true routes through the
// reader service, so a locally DNS-blocked host can still be read.
func TestWebFetchReaderUsesTheReaderHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(writer, "READER CONTENT")
	}))
	t.Cleanup(func() {
		server.Close()
		webReaderBase = "https://r.jina.ai/"
	})
	webReaderBase = server.URL + "/"

	result, err := (&webFetchTool{}).Run(context.Background(), testEnv(t), map[string]any{
		"url":    "https://blocked.invalid/page",
		"reader": true,
	})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "READER CONTENT") {
		t.Errorf("output = %q, want the reader content", result.Output)
	}
}

// TestWebFetchTruncatesALargePage keeps one page from filling the context
// window, which would be reported later as an unexplained trim.
func TestWebFetchTruncatesALargePage(t *testing.T) {
	tool := &webFetchTool{}
	env := testEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		for index := 0; index < 4000; index++ {
			fmt.Fprint(writer, "0123456789")
		}
	}))
	defer server.Close()

	result, err := tool.Run(context.Background(), env, map[string]any{"url": server.URL})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if len(result.Output) > 20200 {
		t.Errorf("output is %d bytes, want it capped", len(result.Output))
	}
	if !strings.HasSuffix(result.Output, "[truncated]") {
		t.Errorf("a truncated page must say so, got the tail %q", result.Output[len(result.Output)-40:])
	}
}

func TestInstallSkillFromLocalDirectory(t *testing.T) {
	tool := &installSkillTool{}
	env := testEnv(t)

	// Use a temporary home so the installed skill cannot leak into other
	// packages' Discover() calls.
	userHome := t.TempDir()
	t.Setenv(config.EnvHome, userHome)

	skillDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: local-skill\ndescription: installed locally\n---\n\nbody"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	result, err := tool.Run(context.Background(), env, map[string]any{
		"source": skillDir,
		"scope":  "user",
	})
	if err != nil || result.IsError {
		t.Fatalf("Run: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "Installed skill") {
		t.Errorf("output = %q, want install confirmation", result.Output)
	}
	if !strings.Contains(result.Output, "local-skill") {
		t.Errorf("output = %q, want skill name", result.Output)
	}
}

func TestInstallSkillRejectsMissingSource(t *testing.T) {
	tool := &installSkillTool{}
	env := testEnv(t)

	result, err := tool.Run(context.Background(), env, map[string]any{
		"source": "",
	})
	if err != nil || !result.IsError {
		t.Fatalf("missing source must fail: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, "source is required") {
		t.Errorf("output = %q, want missing source error", result.Output)
	}
}
