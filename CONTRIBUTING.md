# Contributing to Termixgo

Thanks for taking a look. This file covers the practical steps: how to build,
what the checks are, what the conventions are, and what a change needs to
include.

## Getting set up

Go 1.26 or newer. There is no Node toolchain and no other runtime.

```sh
git clone https://github.com/99apps-id/termixgo
cd termixgo
go build -o bin/termixgo ./cmd/termixgo
./bin/termixgo
```

The first launch opens the onboarding wizard. To point the state somewhere
throwaway while developing:

```sh
TERMIXGO_HOME=/tmp/termixgo-dev ./bin/termixgo
```

## The checks

Everything runs in the terminal. Before you ask for a review, run:

```sh
gofmt -s -l .        # must print nothing
go vet ./...
go build ./...
go test ./...
```

Or the whole gate at once:

```sh
make check                                  # Linux and macOS
powershell -File scripts/check.ps1          # Windows
```

## The race detector

`scripts/race.ps1` and `scripts/race.sh` run `go test -race ./...` and report
whether the result is trustworthy:

```sh
make race                       # Linux and macOS
powershell -File scripts/race.ps1   # Windows
```

It needs cgo, and cgo needs a C compiler. The script finds one and enables cgo
itself; when there is none it names what to install and exits 2, which the
gates treat as "skipped" rather than "failed".

| Platform | Setup |
| --- | --- |
| Windows | `scoop install mingw` (no administrator rights needed) |
| Debian, Ubuntu | `sudo apt install build-essential` |
| Fedora | `sudo dnf install gcc` |
| Alpine | `sudo apk add build-base` |
| macOS | `xcode-select --install` |

CI runs the detector on Linux for every change, so a contribution is verified
even without a local compiler.

A data race is a real bug, not a flake. When the detector reports one, fix the
access rather than retrying the run. The report names both sides of the
conflict, which is usually enough to see which value needs a lock.

`staticcheck ./...` is welcome when you have it installed.

Coverage is reported per package with `go test -cover ./...`. CI enforces a
floor on the total figure, so coverage cannot drift down without someone
deciding to accept it. That floor is a floor, not a target: raise it when the
real number climbs, never lower it to make a change pass.

## What a change needs

1. **A reason.** The commit message or pull request says what problem it
   solves. Not "refactor" but "retry transient provider failures: a 429
   previously lost the whole turn".
2. **A test for user-visible behaviour.** The run loop, compaction, provider
   encoders, tools and trust rules are the ones that must never regress.
3. **No new dependency without a case for it.** The module is deliberately
   small: five direct dependencies. A sixth needs to earn its place in the
   commit message, and anything that pulls in a large tree needs more than
   that.
4. **Comments only where the code cannot explain itself.** A comment says why,
   in one or two lines. No restating what the next line does.

## Conventions

- **No em-dash anywhere**: code, comments, commits, docs. Use a comma, a
  colon, or a full stop.
- **No emojis anywhere.** Same reason: they read as filler in a terminal.
- **Errors are values that name the failure and the next action**, for example
  `no API key for Anthropic; run /setup to add one`.
- **Prefer the standard library.** Reach for a dependency when it is genuinely
  the right tool, not when it saves a few lines.
- **Tests read as sentences.** `TestSecondTurnIsRejectedWhileOneIsInFlight`
  says what it protects; `TestRun2` does not.

## Layout

```
cmd/termixgo        entry point and subcommands
internal/app        shared state, single turn path, Telegram bridge
internal/agent      run loop, tools, prompt, compaction, memory
internal/provider   BYOK clients: OpenAI-compatible, Anthropic, Google
internal/config     ~/.termixgo config.json, defaults and folder trust
internal/secrets    owner-only secret store for API keys and the bot token
internal/skill      SKILL.md discovery and parsing
internal/telegram   Bot API client and the polling companion bot
internal/ui         Bubble Tea model, renderer, setup wizard, plain fallback
```

`TERMIXGO.md` is the project memory and the source of truth for architecture
and invariants. It is also what the agent reads when it works on this
repository, so keep it accurate when you change a rule.

## Adding a tool

Tools live in `internal/agent/tools_*.go`. Implement the `Tool` interface:

- `Name`, `Aliases`, `Description`, `Schema` describe it to the model.
- `Mutating` and `Risk` decide whether the approval policy gates it. Get this
  right: a mutating tool that reports `Mutating() == false` bypasses every
  gate.
- `Label` and `DoneLabel` are the present and past tense phrases shown in the
  transcript.
- `Run` does the work and returns an output string plus, optionally, a plan.

Register it in `DefaultRegistry` in `internal/agent/tools.go`. Add a test that
covers success and at least one failure path.

## Adding a provider

Add an entry to `Providers()` and models to `Models()` in
`internal/provider/registry.go`, including the context window in
`providerContextWindows` or `contextWindows`. Most vendors speak the OpenAI
chat-completions protocol and need no new client; only a genuinely different
wire format needs one.

## Reporting a bug

Include the output of `termixgo doctor`, the version, the platform, and what
you expected. Redact keys: `doctor` never prints them, and neither should you.
