// Package version carries the identity strings the whole binary reports.
//
// Version is a var, not a const, so a release build can stamp it at link time:
//
//	go build -ldflags "-X github.com/99apps-id/termixgo/internal/version.Version=0.2.0"
//
// A const would be folded into every caller and could never be overridden.
package version

// Name is the program name used in banners, usage and logs.
const Name = "Termixgo"

// Version is the semantic version of this build. Release builds override it.
var Version = "0.1.0-dev"

// Commit is the source revision, stamped by a release build when available.
var Commit = ""

// BuildDate is the link date, stamped by a release build when available.
var BuildDate = ""

// UserAgent identifies Termixgo to providers and to the Telegram API. It is
// initialised after the linker has set Version, so a stamped build reports the
// real version here too.
var UserAgent = Name + "/" + Version

// Full reports the version with the revision and date when a release build
// supplied them, which is what makes a bug report traceable to a commit.
func Full() string {
	result := Name + " " + Version
	if Commit != "" {
		result += " (" + Commit + ")"
	}
	if BuildDate != "" {
		result += " built " + BuildDate
	}
	return result
}
