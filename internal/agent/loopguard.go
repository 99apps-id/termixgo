package agent

import (
	"fmt"
	"strings"
)

// loopGuard stops a run that repeats itself instead of progressing. It
// mirrors the Termigo guards: same tool call on repeat, and error streaks.
// The step budget stays the outer bound; this is the inner tripwire that
// fires before the budget burns out.
type loopGuard struct {
	lastCall    string
	repeatCount int
	errorStreak int
	emptyStreak int
	// nudges is how many recovery messages the guard already delivered. The
	// first repetition is answered with a nudge that names what to change; the
	// run only stops when the nudges are exhausted and the loop is still
	// turning. A stop on the third call answered a model that was one line of
	// diagnosis away from succeeding, which is the "agent suddenly stops in
	// the middle of work" complaint.
	nudges int
}

// MaxSameCallRepeats trips after this many identical consecutive tool calls.
const MaxSameCallRepeats = 3

// MaxErrorStreak trips after this many consecutive tool errors.
const MaxErrorStreak = 4

// MaxEmptySteps trips after this many consecutive model steps with no text
// and no tool calls.
const MaxEmptySteps = 3

// MaxLoopNudges bounds the recovery attempts before a genuine loop stops.
const MaxLoopNudges = 2

func callSignature(name, args string) string {
	return strings.ToLower(strings.TrimSpace(name)) + "|" + strings.TrimSpace(args)
}

// noteCall records one tool call and reports whether the run must stop.
// The first repeat answers with a nudge rather than a stop, so the model is
// told what is wrong while it still has budget to fix it.
func (g *loopGuard) noteCall(name, args string) (stop bool, reason string) {
	signature := callSignature(name, args)
	if signature == g.lastCall && signature != "|" {
		g.repeatCount++
	} else {
		g.lastCall = signature
		g.repeatCount = 1
	}
	if g.repeatCount >= MaxSameCallRepeats {
		if g.nudges < MaxLoopNudges {
			g.nudges++
			g.repeatCount = 1
			return false, repeatNudge(name, g.nudges)
		}
		return true, "the same tool call repeated 3 times in a row; stopping instead of looping. Change the arguments or try a different approach."
	}
	return false, ""
}

// repeatNudge is the recovery message delivered instead of a hard stop.
func repeatNudge(tool string, attempt int) string {
	return fmt.Sprintf("You have called %s with the same arguments several times and it is not getting you further. "+
		"Do not call it again unchanged: re-read the file or the error you are working on, name what you assumed that the result contradicts, and choose a different approach. "+
		"Reply \"continue\" if the operator should take over. (recovery attempt %d)", tool, attempt)
}

// noteSuccess clears the repetition state when a call or a step moved the
// work forward. A rerun that succeeds is progress, not a loop: verification
// often repeats a read or a check legitimately, and the old counter let a
// successful rerun arm the tripwire for the next one.
func (g *loopGuard) noteSuccess() {
	g.repeatCount = 0
	g.lastCall = ""
}

// noteResult records whether the tool result was an error. A streak of errors
// does not end the run: the caller delivers the reason as a nudge that asks the
// model to diagnose and try a different approach. The streak resets so the next
// batch starts fresh; the step budget stays the outer bound and the same-call
// guard still catches a genuine loop.
func (g *loopGuard) noteResult(isError bool) (nudge bool, reason string) {
	if isError {
		g.errorStreak++
	} else {
		g.errorStreak = 0
	}
	if g.errorStreak >= MaxErrorStreak {
		g.errorStreak = 0
		return true, "the last 4 tool calls failed in a row. Diagnose the first error above and try a different approach instead of retrying the same call."
	}
	return false, ""
}

// noteEmptyStep records a model step with no text and no tool calls.
func (g *loopGuard) noteEmptyStep() (stop bool, reason string) {
	g.emptyStreak++
	if g.emptyStreak >= MaxEmptySteps {
		return true, "the model produced 3 steps in a row with no text and no tool calls; stopping an idle run. Reply to continue with clearer instructions."
	}
	return false, ""
}

// noteProgress resets the idle counter when the model did something.
func (g *loopGuard) noteProgress() { g.emptyStreak = 0 }
