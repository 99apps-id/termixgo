package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderMarkdownBodyHeadingsAndInline(t *testing.T) {
	out := renderMarkdownBody("# Title\n\nSome **bold** and *italic* with `code`.\n\n- item one\n- item two\n")
	for _, want := range []string{"<h1>Title</h1>", "<strong>bold</strong>", "<em>italic</em>", "<code>code</code>", "<li>item one</li>", "<li>item two</li>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderMarkdownBodyTableAndQuote(t *testing.T) {
	out := renderMarkdownBody("| A | B |\n|---|---|\n| 1 | 2 |\n\n> quoted words")
	for _, want := range []string{"<th>A</th>", "<th>B</th>", "<td>1</td>", "<td>2</td>", "<blockquote>quoted words</blockquote>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderMarkdownBodyMermaidFence(t *testing.T) {
	out := renderMarkdownBody("Intro\n\n```mermaid\ngraph TD\n A --> B\n```\n")
	if !strings.Contains(out, `<pre class="mermaid">`) || !strings.Contains(out, "A --&gt; B") {
		t.Errorf("mermaid block must pass through escaped inside its own pre:\n%s", out)
	}
}

func TestRenderMarkdownBodyEscapesHTMLAndProtectsCode(t *testing.T) {
	out := renderMarkdownBody("Run `<script>` now. Plain<script> text.")
	if strings.Contains(out, "<script>") {
		t.Errorf("script tags must be escaped:\n%s", out)
	}
	if !strings.Contains(out, "<code>&lt;script&gt;</code>") {
		t.Errorf("inline code must stay literal:\n%s", out)
	}
}

func TestRenderMarkdownBodyLinksAndImages(t *testing.T) {
	out := renderMarkdownBody("[**Docs**](https://example.com/a?x=1&y=2) ![A & B](plot.png) [Custom](custom:doc) `[Literal](file.md)`")
	for _, want := range []string{
		`<a href="https://example.com/a?x=1&amp;y=2"><strong>Docs</strong></a>`,
		`<img src="plot.png" alt="A &amp; B">`,
		"<code>[Literal](file.md)</code>",
		"Custom",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, `href="custom:doc"`) {
		t.Errorf("unsupported scheme became an active link: %s", out)
	}
}

func TestRenderMarkdownToolWritesHTML(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	tool := &renderMarkdownTool{}
	result, err := tool.Run(context.Background(), env, map[string]any{
		"text":         "# Hello\n\nBody text.",
		"open_browser": false,
	})
	if err != nil || result.IsError {
		t.Fatalf("Run err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Output, ".termixgo/previews/") {
		t.Fatalf("output = %q, want a preview path", result.Output)
	}
	matches, _ := filepath.Glob(filepath.Join(env.Workspace, ".termixgo", "previews", "*.html"))
	if len(matches) != 1 {
		t.Fatalf("previews = %v, want one html", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil || !strings.Contains(string(data), "<h1>Hello</h1>") {
		t.Fatalf("preview = %q, err=%v", data, err)
	}
}

func TestRenderMarkdownToolReadsFile(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	if err := os.WriteFile(filepath.Join(env.Workspace, "notes.md"), []byte("## From file\n\nDone.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (&renderMarkdownTool{}).Run(context.Background(), env, map[string]any{
		"path":         "notes.md",
		"open_browser": false,
	})
	if err != nil || result.IsError {
		t.Fatalf("Run err=%v result=%+v", err, result)
	}
	matches, _ := filepath.Glob(filepath.Join(env.Workspace, ".termixgo", "previews", "notes-*.html"))
	if len(matches) != 1 {
		t.Fatalf("previews = %v, want one notes html", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil || !strings.Contains(string(data), "<h2>From file</h2>") {
		t.Fatalf("preview = %q, err=%v", data, err)
	}
}

func TestRenderMarkdownToolRejectsEmptyAndOutsidePath(t *testing.T) {
	env := &Env{Workspace: t.TempDir()}
	if result, _ := (&renderMarkdownTool{}).Run(context.Background(), env, map[string]any{"text": "  "}); !result.IsError {
		t.Fatal("empty markdown must error")
	}
	if result, _ := (&renderMarkdownTool{}).Run(context.Background(), env, map[string]any{"path": "../outside.md"}); !result.IsError {
		t.Fatal("outside path must error")
	}
}

func TestRenderMarkdownToolIsRegistered(t *testing.T) {
	registry := DefaultRegistry()
	tool, ok := registry.Lookup("render_markdown")
	if !ok || tool.Name() != "render_markdown" {
		t.Fatalf("render_markdown not registered: %v %v", tool, ok)
	}
}
