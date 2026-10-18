package builtin

import (
	"strings"
	"testing"
)

// TestBuiltinsParseAsSkills pins the discovery contract: each embedded
// document carries valid frontmatter with a name and a description, and a
// non-empty body. A broken builtin fails every Discover call, so it is
// caught here rather than in a workspace test.
func TestBuiltinsParseAsSkills(t *testing.T) {
	if len(Names) == 0 {
		t.Fatal("no builtin skills registered")
	}
	seen := map[string]bool{}
	for _, name := range Names {
		if seen[name] {
			t.Errorf("builtin %q is registered twice", name)
		}
		seen[name] = true
		document, ok := Read(name)
		if !ok {
			t.Errorf("builtin %q cannot be read", name)
			continue
		}
		if !strings.HasPrefix(document, "---\n") {
			t.Errorf("builtin %q has no frontmatter", name)
			continue
		}
		rest := document[len("---\n"):]
		end := strings.Index(rest, "\n---")
		if end < 0 {
			t.Errorf("builtin %q has unterminated frontmatter", name)
			continue
		}
		header := rest[:end]
		body := strings.TrimSpace(rest[end+len("\n---"):])
		if !strings.Contains(header, "name: "+name) {
			t.Errorf("builtin %q frontmatter names the wrong skill:\n%s", name, header)
		}
		if !strings.Contains(header, "description:") {
			t.Errorf("builtin %q has no description", name)
		}
		if body == "" {
			t.Errorf("builtin %q has an empty body", name)
		}
	}
	if _, ok := Read("no-such-skill"); ok {
		t.Errorf("an unknown builtin should not read")
	}
}

// TestBuiltinsHaveDistinctJobs pins the routing the prompt relies on:
// hallmark sets direction for new work, impeccable polishes existing work.
// Two documents that say the same thing would make the two-skill referral
// pointless.
func TestBuiltinsHaveDistinctJobs(t *testing.T) {
	hallmark, ok := Read("hallmark")
	if !ok {
		t.Fatal("hallmark is missing")
	}
	impeccable, ok := Read("impeccable")
	if !ok {
		t.Fatal("impeccable is missing")
	}
	if !strings.Contains(strings.ToLower(hallmark), "redesign") {
		t.Errorf("hallmark should speak to new pages and redesigns")
	}
	if !strings.Contains(strings.ToLower(impeccable), "polish") && !strings.Contains(strings.ToLower(impeccable), "refin") {
		t.Errorf("impeccable should speak to refining existing interfaces")
	}
	if hallmark == impeccable {
		t.Errorf("the two builtin documents must differ")
	}
}
