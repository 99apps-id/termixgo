package whatsapp

import (
	"strings"
	"testing"
)

func TestFormatWhatsAppHeadings(t *testing.T) {
	input := "# Heading 1\n## Heading 2\n### Heading 3"
	got := formatWhatsApp(input)
	want := "*Heading 1*\n*Heading 2*\n*Heading 3*"
	if got != want {
		t.Errorf("formatWhatsApp headings:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestFormatWhatsAppNoDoubleAsterisks(t *testing.T) {
	input := "This is **bold** text and another **item** here."
	got := formatWhatsApp(input)
	if strings.Contains(got, "**") {
		t.Errorf("expected no double asterisks, got: %q", got)
	}
	want := "This is *bold* text and another *item* here."
	if got != want {
		t.Errorf("got: %q, want: %q", got, want)
	}
}

func TestFormatWhatsAppBoldItalicAndStrikethrough(t *testing.T) {
	input := "Notice ***important*** note and ~~deprecated~~ info."
	got := formatWhatsApp(input)
	want := "Notice _*important*_ note and ~deprecated~ info."
	if got != want {
		t.Errorf("got: %q, want: %q", got, want)
	}
}

func TestFormatWhatsAppBulletLists(t *testing.T) {
	input := "- First item\n- Second with **bold**\n* Third item"
	got := formatWhatsApp(input)
	if !strings.Contains(got, "• First item") {
		t.Errorf("expected bullet point, got: %q", got)
	}
	if !strings.Contains(got, "• Second with *bold*") {
		t.Errorf("expected bullet point with formatted bold, got: %q", got)
	}
	if !strings.Contains(got, "• Third item") {
		t.Errorf("expected asterisk bullet to become dot, got: %q", got)
	}
}

func TestFormatWhatsAppPreservesCodeBlocks(t *testing.T) {
	input := "Here is code:\n```go\nfunc main() {\n\t// **not bold**\n}\n```\nAnd outside **bold**."
	got := formatWhatsApp(input)
	if !strings.Contains(got, "// **not bold**") {
		t.Errorf("code block content must not be altered, got: %q", got)
	}
	if !strings.Contains(got, "And outside *bold*.") {
		t.Errorf("outside text must be formatted, got: %q", got)
	}
}

func TestFormatWhatsAppPreservesInlineCode(t *testing.T) {
	input := "Use `**kwargs` and `foo_bar` with **bold** text."
	got := formatWhatsApp(input)
	if !strings.Contains(got, "`**kwargs`") {
		t.Errorf("inline code must preserve double asterisks inside backticks, got: %q", got)
	}
	if !strings.Contains(got, "*bold*") {
		t.Errorf("outside inline code must be bolded, got: %q", got)
	}
}

func TestFormatWhatsAppLinks(t *testing.T) {
	input := "Check our site at [Termixgo](https://termixgo.99apps.id) now."
	got := formatWhatsApp(input)
	want := "Check our site at Termixgo (https://termixgo.99apps.id) now."
	if got != want {
		t.Errorf("got: %q, want: %q", got, want)
	}
}
