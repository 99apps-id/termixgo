# Changelog

All notable changes to Termixgo are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- Folder trust. A fresh folder is untrusted, so writes and commands wait for
  approval; `termixgo trust on` or `/trust on` makes it trusted.
- Approval policy with three modes: `ask`, `edits`, `all`.
- Onboarding wizard, run automatically on first launch or with `/setup`:
  provider, server endpoint when the provider has no fixed host, API key,
  default model, optional Telegram bot.
- Slash commands: `/model`, `/setup`, `/help`, `/new`, `/sessions`, `/stop`,
  `/status`, `/trust`, `/approval`, `/plan`, `/tools`, `/skills`, `/memory`,
  `/telegram`, `/init`, `/cost`, `/exit`.
- Streaming transcript that distinguishes thinking, reasoned, tool calls and
  tool results, with the plan rendered inline.
- The tool set: filesystem, search, exact-match editing, command execution,
  project check detection, todos, learned memory, skills, questions,
  subagents and web fetch.
- `search_memory`: SQLite FTS5 full-text search over learned memory, the error
  journal and indexed workspace files, with `memory`, `journal` and `workspace`
  scopes. This is the one feature that carries a bundled database engine, a
  pure-Go build of SQLite, so the binary stays cgo-free.
- Telegram companion bot with pairing, owner pinning and per-command handling.
- Plain (non-TTY) mode so `termixgo run` composes with pipes and CI.
- Per-model context budgets, retry with backoff, a stream stall watchdog and a
  single-turn gate.
- `doctor` reports who can read the secret file, which is verified rather than
  assumed; the file is protected with an explicit ACL on Windows instead of the
  inherited one.
- Estimated cost per session in the status bar and `/cost`, with an optional
  `costBudgetUsd` cap that stops a run before it passes the limit and says so.
- `Ctrl+C` and `SIGTERM` cancel the turn in flight and save state, instead of
  killing the process.
- A one-shot `termixgo run` no longer writes a session file, so it is safe to
  call in a loop or from CI.
- Release builds stamp the version, commit and build date through ldflags.
- `modelPricing` in the config supplies token prices for models the built-in
  table does not know, which is what makes a cost budget usable behind a custom
  endpoint. `doctor` reports how many overrides are in effect.
- Background processes: `run_background`, `run_logs`, `run_wait`, `run_list`
  and `run_kill`, with `/ps` to inspect them from the terminal. Stopping a
  process stops its children, via a job object on Windows and a process group
  elsewhere, so "stopped" means the port is free.
- Git tools: `git_status`, `git_diff`, `git_log`, `git_show`, `git_add`,
  `git_commit`, `git_branch` and `git_restore`. Git runs with explicit
  arguments rather than a shell, and `git_restore` refuses a pathless revert.

### Fixed

- `termixgo -p` no longer loses its output. The event drain was stopped with a
  deferred close as soon as the turn returned, which could fire before the
  printer goroutine had run once. A one-shot run printed nothing at all, and a
  truncated tail was possible even when it printed something.
- `termixgo -p` now exits non-zero when the turn failed. A rejected request is
  reported by the run loop as an event with a nil error, so a script that ran
  the agent in a pipeline saw success for a turn that never happened.
- `run_kill` now names the recovery path when the handle is unknown. `run_logs`
  and `run_wait` answered with "use run_list to see them" while `run_kill` said
  only that the handle did not exist, so a mistyped handle left the agent with
  nothing to retry with.
- A cost cap set for a model with no recorded price is now called out instead of
  reported as if it were working. `doctor` prints a warning naming the fix and
  `/cost` appends "which cannot be enforced until this model has a price", so the
  operator does not discover the gap from a bill.
- `termixgo model <provider>:<id>` and `/model <provider>:<id>` now record the
  same thing. The command line stored the prefixed spelling while the app stored
  the bare id, so `doctor`, the status bar and a `modelPricing` override could
  disagree about the same model. Both go through one resolver now.
- Four `staticcheck` findings are fixed, which matters because the CI job that
  runs it fails the build: a `fmt.Sprintf` with no arguments, two error strings
  that opened with a capital letter, and a struct field in a test that nothing
  read.
- A test asserted the Windows spelling of the secret file's access detail
  (`"1 entries"`), so it failed on Linux, where the detail is a mode. It now
  checks the evidence that matches the platform. This was found by cloning the
  repository inside WSL and running the suite there, which is now documented in
  `CONTRIBUTING.md`.

### Build

- The repository has line-ending rules in `.gitattributes` instead of relying on
  `core.autocrlf`. A checkout on Linux or macOS now gets LF for every source,
  shell and build file, which is what a correct `make check` and a readable
  `gofmt` need. `*.ps1` stays CRLF because PowerShell's here-strings and batch
  parsing are line-ending sensitive. The `Makefile`, the docs and the workflow
  files were CRLF in the working tree and are LF now.

### Security

- The secret file and its state directory grant access only to the owner, on
  all three platforms. On Windows this previously inherited the parent
  directory's grants, which could let another local account read API keys.
- A saved session grants access only to the owner too. The file was written with
  mode 0600, which is a no-op on Windows, so a conversation could be readable by
  another local account. That matters because a conversation can contain a key
  the user pasted into it. `Save` now applies the same restriction as the secret
  file, on the temporary file and again after the rename.
- A data race on the session was found by the race detector and fixed: the
  conversation was written by the agent goroutine while the terminal read it
  for the status bar and the transcript. `Session` fields are now unexported
  behind locked accessors, so an unlocked read does not compile.
- `scripts/race.ps1` and `scripts/race.sh` run the detector and enable cgo
  themselves, with setup instructions when no C compiler is present.
- The Telegram poll loop no longer re-dispatches a replayed update. The offset
  guard only covered the offset itself, so an update Telegram resent was handed
  to a handler a second time and the command ran twice. The gate also cross
  builds for Linux and macOS now, which catches a build tag that only breaks
  on a platform nobody tested locally.

## 0.1.0-dev

Initial development. Not released.
