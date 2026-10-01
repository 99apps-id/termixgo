# TERMIXGO.md

Project memory for Termixgo. Termixgo reads this file at startup and puts it in
the agent's system prompt, so keep it accurate and short.

## What this is

Termixgo is a coding agent that runs entirely in a terminal. No web view, no
browser, no runtime beyond the compiled binary. Go 1.26, Bubble Tea for the UI,
providers called over plain HTTPS.

Two sibling repositories are the reference for behaviour and are read-only from
here: `c:/project/termigo` and `c:/project/termigo-neo` (Tauri + React). Those
are the real directory names on disk, so they are the only remaining "termigo"
spellings in this repository; anything belonging to this project is Termixgo.
Where this project and those differ, this project's README is the source of
truth for Termixgo.

## Quality bar

- Correctness first: no "works for now". A bug in the run loop or the provider
  clients is worse than a missing feature.
- Memory discipline: the binary must stay comfortable on a 2 GB machine. The
  process should idle well under 100 MB and never approach 500 MB. Do not add a
  dependency that pulls in a large transitive tree without a clear reason.
- Security: provider keys and the Telegram token live in
  `~/.termixgo/secrets.json` with mode 0600 and nowhere else. Never log a
  secret, never write one into `config.json`, a session file or the transcript.
- Terminal UX: every state has to read well in 80 columns, in a light and a dark
  theme, and with colour disabled.

## Conventions

- No em-dash anywhere: code, comments, commits, docs.
- No emojis anywhere.
- Comments explain why, never what. If a line needs a "what" comment, rename
  things instead.
- Errors are values that name the thing that failed and the next action.
- Prefer the standard library. The direct dependencies are `bubbletea`,
  `bubbles`, `lipgloss`, `golang.org/x/term`, `golang.org/x/sys` and
  `yaml.v3`, plus `modernc.org/sqlite` for the full-text index behind
  `search_memory`. That one is pure Go, so it keeps the build cgo-free, and it
  is the single largest transitive tree in the module: weigh it against the
  memory budget before adding anything that pulls in more.

## Layout

```
cmd/termixgo        entry point and subcommands (run, models, doctor, secret, ...)
internal/app        shared state: config, provider client, session, Telegram bridge
internal/agent      the tool-calling run loop, tools, prompt, compaction, memory
internal/provider   BYOK clients: OpenAI-compatible, Anthropic, Google
internal/config     ~/.termixgo config.json, defaults and folder trust
internal/mcp        MCP client: stdio JSON-RPC, tool listing and dispatch
internal/secrets    0600 secret store for API keys and the bot token
internal/search     SQLite full-text index over memory, journal and workspace
internal/skill      SKILL.md discovery and parsing
internal/telegram   Bot API client and the long-polling companion bot
internal/ui         Bubble Tea model, renderer, setup wizard, plain fallback
```

## Agent invariants

- `agent.Runner.Run` owns the loop: stream, execute tools, repeat, bounded by
  `MaxSteps`, the whole per-turn ceiling. Every step reports through
  `Env.Emit`. The ceiling is one explicit number, not a hidden multiple: the
  operator's `maxSteps` is exactly how many steps one reply may run, and the
  turn pauses there while the loop guard and the cost cap stay the real stops
  for a run that goes nowhere. A subagent sets its own smaller `MaxSteps` and
  keeps a hard budget.
- Any string bound in `internal/agent` goes through `clipBytes` or
  `clipTailBytes`. A byte slice at a fixed offset can land inside a multi-byte
  character, and the terminal then paints a replacement glyph. The same rule
  holds for a Telegram body, where the consequence is worse: the Bot API rejects
  a body that is not valid UTF-8, so the message is never delivered.
- The TUI frame must fit the terminal box. Bubble Tea drops the top lines of a
  taller frame and moves the cursor relative to the previous frame, so one row
  too many shifts the whole screen and the operator reads earlier lines in the
  wrong place. Every list overlay is windowed, the approval diff takes only the
  rows left over, and `View` clamps as a last line of defence. The status line is
  built in two groups for the same reason: the token count and the spend are
  placed last and kept, and the identity parts are dropped to make room.
- A slash command is a local control and is never handed to the model as text.
  Read-only commands and the ones that act on the live turn run while a turn is
  in flight; a command that would start work, such as `/new`, is queued and runs
  as a command when the turn ends.
- `search_memory` is backed by the FTS5 store in `internal/search`, which the app
  opens at `<workspace>/.termixgo/search.db`. The database is state, not content:
  the workspace walk skips that directory, and a checkpoint stash excludes it.
  The index has no timer; it refreshes before a search, so an idle session pays
  nothing.
- The approval decision is taken before a mutating tool runs, through
  `Env.Approve`. The desktop build has no gates; Termixgo adds real ones, so
  trust and `ApprovalMode` must be honoured.
- Tool schemas are JSON Schema objects. `edit` matches exact strings: never
  change it to line-based editing without a matching change to the prompt.
- `read_file` returns line-count metadata, not inline line numbers, so an exact
  match stays copyable.
- Compaction targets 50 to 60 percent of the budget and must never leave a tool
  result at the head of the message list. The budget comes from
  `provider.Model.Window()` through `agent.HistoryBudget`, never from a global
  constant.
- A transient HTTP failure is retried only before the response body is read.
  Once a caller starts consuming streamed chunks the request has produced
  output, so a retry would double-apply it.
- A provider that goes silent must fail. The stall guard in the provider
  package is what turns a hung connection into an error; a bare
  `http.Client` timeout cannot see it.
- One turn runs at a time across the whole app, acquired with `runMu.TryLock`.
  A second caller gets `app.ErrBusy`; it is never queued, because a queued
  caller looks hung to the operator.
- Telegram update handlers run on their own goroutines. A blocking poll loop
  would stop `/stop` from arriving during a run, which is the one moment it is
  needed. The offset is "the first update Telegram should return", so an update
  at the offset is new and one below it has already been handled: skipping is
  what stops a replayed command running twice, and the skip has to cover the
  dispatch, not just the offset bookkeeping.
- Cost is estimated from a price table, never from a provider-reported figure,
  because no provider reports money. A budget stops the run before the step
  that would pass it and names the amount in the message. `Pricing.Known()`
  separates "free" (a local model) from "unpriced", and a budget of zero means
  unlimited rather than "stop at once".
- `App.SetEphemeral(true)` is what stops a one-shot `termixgo run` writing
  session files. Persistence is the default for a conversation; a command is
  not a conversation.- `Session` is written by the goroutine running a turn while the terminal keeps
  reading it, so its fields are unexported and every access goes through a
  method that takes its mutex. That is deliberate: an unlocked read does not
  compile. The race detector found this the first time it was run, so keep it
  running. `Messages()` and `Todos()` copy the slice header; the elements are
  never mutated after being added, which is what makes sharing them safe.- Cancellation flows through a context: `signal.NotifyContext` in the command,
  through `App.RunTurn`, into `Runner.Run`, and out to the HTTP request. The
  run loop checks the context between steps, so a stop is prompt and saves
  state instead of abandoning it. Nothing may block on a context the caller
  can cancel.
- A background process must survive the turn that started it, so the manager
  lives on the app and its emitter is installed once, not per run. Killing a
  handle must kill the tree: a job object with kill-on-close on Windows, a
  process group elsewhere. `cmd.WaitDelay` bounds the wait for the output pipe,
  which a surviving grandchild would otherwise hold open forever. Shutdown
  waits for the processes to go, because the caller exits immediately after.
- Git runs through `exec.Command` with explicit argv, never a shell: arguments
  come from the model, and a file name or commit message must stay literal
  text. `git_restore` without paths is refused, since it would discard a whole
  repository's work.
- Cost has three states, not two: a known price, a known zero for a local
  model, and unknown. `Model.CostModel` returns both the price and whether it
  is known, so a zero next to a local model is never confused with "unpriced
  and therefore unbudgetable".
- An MCP server is a process this program did not write, so it is trusted no
  further than its protocol. Its tools are always prefixed `mcp_`, which is what
  makes a hijack of a built-in name impossible: the registry indexes by name, so
  an unprefixed contribution could take over a `write_file` call. Its read-only
  hint may lower the gate and never raise it, so a tool that claims nothing is
  treated as one that can change things. Connecting is best effort and the
  handshake is bounded: one broken server must not cost the operator the session
  or the other servers.

## Checks

Run these before claiming a change is done:

```
gofmt -s -l .
go vet ./...
go build ./...
go test ./...
go test -race ./...   # needs cgo; skipped with a notice where absent
```

`make check` (or `scripts/check.ps1` on Windows, `scripts/check.sh` elsewhere)
runs the same set. `staticcheck ./...` is welcomed when installed.

The agent can also run the host project's own checks against whatever is being
worked on: `biome`, `vitest`, `cargo`, `pytest` and so on are ordinary shell
commands, executed through the `run_command` tool and reported in the
transcript.
