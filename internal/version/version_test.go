package version

import (
	"strings"
	"testing"
)

func TestFullIncludesTheVersionAlways(t *testing.T) {
	full := Full()
	if !strings.Contains(full, Name) {
		t.Errorf("Full() = %q, want it to name the program", full)
	}
	if !strings.Contains(full, Version) {
		t.Errorf("Full() = %q, want it to carry the version", full)
	}
}

// TestFullAddsTheRevisionWhenStamped is what makes a bug report traceable: a
// release build sets Commit and BuildDate through ldflags, and the reported
// string has to show them.
func TestFullAddsTheRevisionWhenStamped(t *testing.T) {
	originalCommit, originalDate := Commit, BuildDate
	t.Cleanup(func() { Commit, BuildDate = originalCommit, originalDate })

	Commit = "abc1234"
	BuildDate = ""
	stamped := Full()
	if !strings.Contains(stamped, "abc1234") {
		t.Errorf("Full() = %q, want the commit in it", stamped)
	}
	if !strings.Contains(stamped, "(") {
		t.Errorf("Full() = %q, want the commit parenthesised", stamped)
	}
	if strings.Contains(stamped, "built") {
		t.Errorf("Full() = %q, want no build date when none was stamped", stamped)
	}

	BuildDate = "2026-01-02T15:04:05Z"
	withDate := Full()
	if !strings.Contains(withDate, "2026-01-02") {
		t.Errorf("Full() = %q, want the build date in it", withDate)
	}
	if !strings.Contains(withDate, "built") {
		t.Errorf("Full() = %q, the date should be labelled", withDate)
	}
}

// TestDevelopmentBuildHasNoDecoration keeps a plain build readable.
func TestDevelopmentBuildHasNoDecoration(t *testing.T) {
	originalCommit, originalDate := Commit, BuildDate
	t.Cleanup(func() { Commit, BuildDate = originalCommit, originalDate })

	Commit = ""
	BuildDate = ""
	plain := Full()
	if strings.Contains(plain, "(") || strings.Contains(plain, "built") {
		t.Errorf("Full() = %q, want no decoration for an unstamped build", plain)
	}
	if plain != Name+" "+Version {
		t.Errorf("Full() = %q, want %q", plain, Name+" "+Version)
	}
}

// TestUserAgentNamesTheProgramAndVersion is what providers and the Telegram API
// see. It is built from Version at package init, so it must not be stale.
func TestUserAgentNamesTheProgramAndVersion(t *testing.T) {
	if !strings.HasPrefix(UserAgent, Name+"/") {
		t.Errorf("UserAgent = %q, want it to start with %q", UserAgent, Name+"/")
	}
	if !strings.Contains(UserAgent, Version) {
		t.Errorf("UserAgent = %q, want version %q in it", UserAgent, Version)
	}
	// A user agent must be a single token: a space would break the header.
	if strings.ContainsAny(UserAgent, " \t\r\n") {
		t.Errorf("UserAgent = %q, want no whitespace", UserAgent)
	}
}

func TestNameIsTheProjectName(t *testing.T) {
	if Name != "Termixgo" {
		t.Errorf("Name = %q, want Termixgo", Name)
	}
}
