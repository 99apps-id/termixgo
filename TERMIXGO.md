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
  OAuth access and refresh tokens live there too, under `oauth:<provider>`. A
  vendor's public installed-app client pair is never committed: a release build
  stamps the Antigravity pair into the binary with ldflags, from a git-ignored
  `.env.local` or the environment, and an unstamped build prompts and stores it.
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
- A read-tier verdict with zero tool calls behind it is prose, not a review.
  `agent.RunSubagent` counts the nested run's assistant tool calls: an empty
  pass for a read-only role is retried once with an explicit mandate, and a
  second empty pass comes back as an error so it can never read as an
  approval. Worker roles keep the plain answer, because a general task can be
  settled from the prompt alone.
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
  as a command when the turn ends. The command menu is opened deliberately: a
  slash typed while composing stays literal, and only a bare slash committed
  with Enter opens the palette. Typing never summons the menu, so a path such as
  `c:/project` or prose that contains a slash stays intact.
- `search_memory` is backed by the FTS5 store in `internal/search`, which the app
  opens at `<workspace>/.termixgo/search.db`. The database is state, not content:
  the workspace walk skips that directory, and a checkpoint stash excludes it.
  The index has no timer; it refreshes before a search, so an idle session pays
  nothing.
- The approval decision is taken before a mutating tool runs, through
  `Env.Approve`. The desktop build has no gates; Termixgo adds real ones, so
  trust and `ApprovalMode` must be honoured. The untrusted-folder gate must
  still honour the operator's answer AT that gate: session and folder-always
  answers ride on `Env.SessionAllowed` (re-seeded every turn from the app's
  folder grants, folder-always persisted per folder in config) - an answer
  the gate never reads is a dead promise. Tool-level allowances are not that
  answer: they speak about a tool, not about this folder.
- The edit diff preview is computed at that same pre-run moment and travels
  on `EventToolStart`: once the tool has succeeded the old text is gone and
  a late preview would describe a change that cannot be found anymore. The
  transcript colors it only for a finished, successful tool - running or
  failed edits show no preview. Compact view peeks a few unified lines; the
  details view goes two-column (the /diff renderer) on wide terminals and
  stays unified when the width cannot hold two readable columns.
- The wordmark has one source of truth: the block glyphs and per-letter hues
  in `internal/ui/banner.go`. The README banner (`termixgo-banner.svg`) and the
  welcome screenshot (`termigo-welcome.png`) mirror that paint. Change the TUI
  banner first and re-derive the README assets from it, never the other way.
- Tool schemas are JSON Schema objects. `edit` matches exact strings: never
  change it to line-based editing without a matching change to the prompt.
- `read_file` returns line-count metadata, not inline line numbers, so an exact
  match stays copyable.
- Compaction targets 50 to 60 percent of the budget, must never leave a tool
  result at the head of the message list, and must always hand back a history
  that fits. That last one outranks keeping the newest messages whole: older
  turns are elided first and the tail given up last, but a trimmed tool result
  only costs detail while a request over the window is refused, and a refused
  request stops the session for every step after it. The budget comes from
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
  never mutated after being added, which is what makes sharing them safe.- A scheduled job and the heartbeat run through the same runner as an operator
  turn, but on a throwaway session that is never saved: a background turn must
  not appear in the operator's conversation or rewrite the live session file.
  Only a real answer is delivered; an empty reply, `[SILENT]` or `HEARTBEAT_OK`
  is dropped. The scheduler advances a job's `NextRun` before it runs, so a
  restart replays nothing, and the jobs file is re-read each tick so the CLI can
  edit it while `serve` runs.
- Generated skill content is staged, never written live. `propose_skill` records
  a proposal under `.termixgo/skill-proposals/`; only the operator applies it
  with `/skills apply`, and an update whose target hash moved since the proposal
  is refused as stale. A path that writes the live `SKILL.md` directly would
  defeat that, so the write stays behind the operator.
- A background coding worker is an external process owned by `ProcessManager`,
  never a second in-process turn: the run loop is single-turn by design, so a
  worker has to be detached to survive the turn. It runs in its own git worktree
  so it cannot edit the main tree; the worker argv is explicit, never a shell
  string, a missing binary is named before a worktree is created, and its
  completion is an `EventProcessEnd` the app forwards to chat. A native worker
  (`termixgo`) runs this same binary and needs no external account, and it sets
  `TERMIXGO_WORKER_DEPTH` in the child so a worker cannot start another worker.
- `parallel_batch` fans independent tasks over that same machinery, one worker per
  worktree, capped so a single call cannot flood the process table or the disk.
  Every task is resolved, its worker binary found and its name checked against the
  other tasks before any checkout exists, because a batch that fails halfway would
  leave half the checkouts behind and the operator guessing which half. Worktree
  creation is serialised, since the registry is one read-modify-write JSON file,
  while the workers themselves run in parallel. Approval is the operator's only
  brake on a fan-out, so the tool label names the worker kinds and not just the
  count. The detached process keeps its own context on purpose: the caller's
  context, and a slash command's timeout with it, must not end a worker.
- `/diff` is a rendering path, not a turn: it reads `git_diff` output and draws
  numbered rows side by side, falling back to the single column when the terminal
  is too narrow for two. It is not fed back to the model.
- Web search is keyless-first but key-aware: a configured Tavily or Brave key
  is tried before the keyless DuckDuckGo/Wikipedia/GitHub chain, because an ISP
  can block one hostname by DNS without blocking a paid API on another domain.
  A DNS failure is not retried, since the name will not resolve on a second
  attempt; only a transient connect failure is. An empty result from a source
  that answered is never reported as the machine being offline. `web_fetch`
  retries through r.jina.ai automatically when a direct fetch cannot resolve or
  is bot-blocked, and `reader: true` forces the reader for a JavaScript page, so
  a host a local DNS block hides still reads without the model knowing the flag.
  A question with a fixed source (weather, exchange rate, crypto price, a
  Wikipedia summary) goes through the `lookup` tool's keyless endpoints, not a
  search: routing those through a search returns a list where the source would
  return the answer. The web transport resolves a host over DNS-over-HTTPS when
  the system resolver fails, so an ISP DNS block does not take the keyless paths
  with it; the fallback runs only on a resolution failure, never on the happy
  path.
- Cancellation flows through a context: `signal.NotifyContext` in the command,
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
- A provider's live model list outranks this build's catalogue. Meta answers the
  same `404 model_not_found` for a stale key, for its own overload and for a
  model it refuses to serve, and Muse Code availability moves with the source
  address of the request, not only with the account: one login lists the
  `muse-spark` family from a residential IP and lists only `muse-image-1.0` from
  a datacenter one. So a persistent 404 is explained by reading `GET /v1/models`
  before naming a cause, and the registry states what the wire accepts, never
  what a given account or address is entitled to.
- The agent-event observer is one slot with one owner. `App.SetObserver` returns a
  claim and refuses to install over a live one, so a second surface cannot detach
  the sink of the turn that is streaming; the holder releases it with
  `App.ClearObserver(claim)`, and a claim of 0 releases nothing. That is what stops
  a caller which lost the run race from clearing the slot on its way out. A refused
  install is not an error: the caller runs and streams nothing, which is acceptable
  because that turn is refused as busy by `runMu` anyway, the slot guarding the same
  single run that lock guards. Passing nil force-clears and exists so a test can
  switch the slot off without holding one, never as a run path. Every install site
  defers its release, since a claim that is never released leaves the slot occupied
  for the life of the app.
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
