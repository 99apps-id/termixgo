# AGENTS.md

Read `TERMIXGO.md` first. It is the project memory and the source of truth for
Termixgo's architecture and conventions. Where anything conflicts, `TERMIXGO.md`
wins.

## Quick facts

- Termixgo is a Go 1.26 terminal agent. `cmd/termixgo` is the entry point,
  `internal/` holds the packages.
- Package manager: Go modules. There is no Node toolchain in this repository.
- The UI is Bubble Tea. The fallback for non-terminals is `ui.RunPlain`.
- Two sibling repositories are the behavioural reference and are read-only:
  `c:/project/termigo` and `c:/project/termigo-neo`. Their names are the real
  directory names on disk and are deliberately left unchanged; everything that
  belongs to this project is spelled Termixgo.

## Agent guidelines

1. Work autonomously: inspect, edit, build and test without asking for
   permission on routine steps.
2. Ground every claim in a real file and line, or in real command output. Never
   invent an import, a path or a test result.
3. Keep the module dependency-light. A new dependency needs a reason in the
   commit message.
4. Every user-visible behaviour gets a test. The run loop, compaction, the
   provider encoders and the trust rules are the ones that must never regress.
5. No em-dash and no emojis anywhere.

## Checks to run

```
gofmt -s -l .
go vet ./...
go build ./...
go test ./...
```

Or `make check`. On Windows, `powershell -File scripts/check.ps1`.

## Working in the terminal

Everything in this project is done from a terminal: editing, building, testing
and verification. Termixgo itself is the tool for that work when you want the
agent to drive it, and it streams thinking, tool calls and command output as it
goes.
