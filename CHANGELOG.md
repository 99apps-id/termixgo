# Changelog

All notable changes to Termixgo are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- `parallel_batch` starts several coding workers at once, one per git worktree,
  each detached so it survives the turn, and announces every completion in chat.
  The terminal gains `/batch <task> :: <task>` for a line of independent work,
  `/workers` for the state of each tracked checkout (running, done, failed or
  idle), and `/diff [side|unified]`, which draws the working tree as two columns
  and falls back to one column when the terminal is too narrow to keep both
  readable. A batch is checked through before it starts: an unknown worker, a
  worker binary that is not installed, an empty task or two tasks claiming the
  same worktree name is refused with nothing created, and the approval prompt
  names the worker kinds because one confirmation starts all of them.
- Two Muse Spark ids the live service offers and this build did not know:
  `muse-spark-1.2-contributor` and `muse-spark-1.1`. Their published context
  windows are unknown, so they are pinned to the conservative default instead of
  inheriting the family's 1M: an over-large budget makes Meta reject the request,
  an over-small one only shortens the history the operator keeps. `sam-3.1` and
  `muse-voice-transcribe-1.0` also appear in the Muse model list and are
  deliberately not offered here, because they are not chat models.

### Fixed

- A persistent Muse 404 now says which of its causes it is. Meta answers a stale
  key, its own overload and a model it refuses to serve with the same
  `404 model_not_found`, and the hint blamed the subscription and the base URL
  whatever the reason. The client reads `GET /v1/models` before explaining the
  failure. When that list omits the requested id, the error names the ids that
  are offered and says the catalogue changes with the source address, because one
  login can list `muse-spark` from a home network and only `muse-image` from a
  datacenter server. When the list includes the id it keeps the tier and
  custom-endpoint checks and says the entitlement is not the problem. When the
  list cannot be read it admits that instead of implying it had looked.
- The Muse mint's `user_email` is kept as the account id and named in that error.
  Two machines on one subscription behave differently, and which account each
  stored key belongs to is the fact that tells them apart; it used to be dropped
  with the rest of the identity fields.
- Compaction now always hands back a request that fits the model's window. The
  newest four messages were beyond its reach, so a single large recent tool
  result (a `read_file` at its 64 KB cap costs about 25k tokens against the
  9.6k history budget of a 32k model) could not be trimmed at all: every later
  step was rejected by the provider, the failure looked unrelated to anything
  the operator asked, and only starting a new session cleared it. Older turns
  are still elided first and the tail given up last, and a pass that shortens
  what remains bounds the result by construction rather than by hope.
- An interrupted turn no longer reports that it spent nothing. The clean stop
  carried the turn's summed usage on `EventTurnEnd` and the step-cap, loop
  guard, aborted and error paths did not, so the audit ledger, which reads
  usage off exactly that event, recorded a thirty-step turn as a free one.
  Every stop path now reports the same total.
- A session file can no longer decide where the next save writes. `LoadSession`
  confined the id it was asked for and then adopted the id stored inside the
  file, and `Save` joined that into the sessions directory unchecked, so a
  session whose text carried `../../pwned` wrote its transcript one level above
  the directory that holds it on the first turn after it loaded. `Save` applies
  the guard a read applies, and a loaded session keeps the validated id.
- An OpenAI-compatible gateway error inside a 200 response now fails the turn.
  Hosts that report a failure as a stream frame were decoded into a chunk with
  no choices and ignored, so the agent got a half-finished answer with a nil
  error and a rejected request read as the model choosing to stop early. The
  object and the bare-string form are both recognised, and a frame that carries
  no error is still tolerated, so a server sending fields this client does not
  model keeps working.
- Anthropic usage is counted once per request. Its counters are cumulative for
  the request and one response can carry more than one message, because
  interleaved thinking is in the OAuth beta header list, while the agent sums
  every usage event it is handed, so each extra message charged the same prompt
  tokens again. The OpenAI reader already deduplicated this way; the Anthropic
  reader now uses the same guard.
- The Telegram and scheduler event stream belongs to the turn that claimed it.
  The observer was one global slot that any caller could overwrite and any
  caller could clear, so a second inbound message that lost the single-run race
  also cleared the observer of the turn still streaming: the chat sat on
  "Working...", progress stopped arriving, and the prompt returned an empty
  answer because the error notice had nowhere to go. A claim is refused while
  the slot is held, and only its holder can release it.
- `edit` no longer rewrites the line endings of lines it never touched. CRLF was
  treated as a property of the whole file, so a file mixing both terminators
  had every bare LF written back as CRLF and a one-line change arrived as a
  whole-file diff. Matching is done against the file's own bytes, and the
  replacement inherits the terminator of the region it replaced.
- A rejected approval mode names every mode this build accepts. `plan` was a
  valid mode while the error still listed ask, edits and all, so an operator who
  mistyped it was told the mode did not exist. The test now reads the message
  against the list this build accepts, so a mode cannot be left out again.

## 0.1.3 - 2026-10-03

### Fixed

- Meta Muse Code survives its intermittent overload. A request that answers
  `404 model_not_found` is retried up to three times with the same key, with
  backoff and jitter, before the key is treated as stale and minted again. Meta
  uses that status both for an overloaded backend and for an aged-out key, so
  the transient case no longer triggers a needless re-mint or a failed turn.

## 0.1.2 - 2026-10-03

### Fixed

- Meta Muse Code failed after the first exchange with `404 model_not_found`.
  A replayed `function_call` input item now carries the `id` and `status` Meta
  requires, and a stale key (which Meta reports as `404`, not `401`) is minted
  again and the request replayed once before giving up.

## 0.1.1 - 2026-10-02

### Added

- Meta Muse Code as a login provider and model backend. `termixgo login muse`
  runs Meta's RFC 8628 device flow, mints the returned device token into an LLM
  API key, and stores it. `muse` speaks the Responses API at `api.meta.ai`
  (the same shape as Codex) with the Muse identity headers, and the catalogue
  offers `muse-spark-1.3`, `muse-spark-1.3-contributor` and `muse-spark-1.2`.
  A login is a subscription, so its models report no per-token dollar price.

### Fixed

- Release archives carry the install scripts again: `INSTALL.bat`,
  `install.ps1` and `uninstall.ps1` in the Windows zip, `install.sh` in the
  tar.gz for Linux and macOS. The goreleaser configuration only listed the
  docs, so a download from the releases page had the binary but nothing to
  install it with, unlike the bundle `scripts/make-installer.ps1` builds.

## 0.1.0 - 2026-10-02

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
- A slash command whose argument is a fixed set offers those values as a menu.
  `Tab` on `/trust` in the composer, or `/trust ` and `Enter`, lists the choices
  with a line on what each one does. Choosing a value runs the command; a value
  that only starts an argument, such as `delete` for `/sessions`, fills the
  composer and waits for the rest. Before this the menu only printed the current
  state, so an operator who reached a command through the menu had to know its
  arguments from memory.
- An interactive turn continues into the next step segment while the previous
  one was still dispatching tools, so a small `maxSteps` no longer pauses a task
  that is making progress. The loop guard and the cost cap stay the inner stops
  and the segment count is the outer ceiling; a subagent keeps a hard budget.
- `search_memory` takes an optional `path`, so a search over a large workspace
  can be limited to the folder being worked on instead of the whole tree. The
  workspace index also skips dependency and build trees, and caps how many files
  one refresh indexes, so a search on a big tree no longer walks tens of
  thousands of generated files.
- `search_memory` is registered and the full-text index behind it is opened, so
  the tool is reachable and the index is used. It was written, documented and
  tested but never added to the registry, so the model could not call it and
  every search fell back to a substring scan over memory and the journal.
- Two plan endpoints are first-class providers instead of a hand-configured
  OpenAI-compatible endpoint: StepFun Plan
  (`https://api.stepfun.ai/step_plan/v1`) and Qwen Cloud Token Plan
  (`https://token-plan.maas.qwencloudapi.com/compatible-mode/v1`), each with its
  own catalogue models. The token plan lists Qwen3.8 Max, Qwen3.8 27B, Qwen3.7
  Max, Qwen3.8 Flash, Qwen3.6 Flash, DeepSeek V4.1 Flash, DeepSeek V4 Pro (and
  the pinned 0813 build), DeepSeek V4 Flash 0731, GLM 5.3 and GLM 5.2. Their ids
  are host-prefixed so a catalogue entry never collides with the vendor's own.
- `termixgo endpoint [provider url]` shows or sets a provider's custom base URL
  from the terminal, which is what a self-hosted or account-scoped server needs
  when the address was set in another application.
- GitHub pull requests through the `gh` CLI: create, view, list, review, comment
  and merge. A mutating call is gated by the approval policy, and the arguments
  go through argv rather than a shell, exactly like the git tools.
- Orchestration pipelines: `orchestrate` runs the steps in
  `.termixgo/pipelines/<id>.json` as subagents in dependency order, with parallel
  steps and `{{step.field}}` interpolation, and `list_pipelines` names them.
- `read_image` attaches a local png, jpg, gif, webp or bmp to the conversation so
  a vision model can see a screenshot, mockup or chart. The images ride on a user
  message after the tool results, because providers accept them there and not on
  a tool message.
- `find_tools` searches the toolset by keyword and loads the match for the rest
  of the turn. `toolSearchEnabled` in the config keeps the ecosystem tools
  (GitHub, pipelines, images, skills, memory, web) out of every request and
  loads them on demand, which trims the tool schema a large toolset sends every
  step.
- A call to a tool that does not exist now answers with near matches and, when
  tool search is on, how to discover one, instead of a bare name-not-found.
- The Telegram `/model` command opens a picker: one inline button per provider,
  then one per model, and a press switches the model. It used to print only the
  active model, so choosing another meant knowing its id. `/model <id>` still
  works for a direct switch.
- The Telegram progress card keeps the whole turn instead of only the newest
  line, so the task no longer scrolls away, and a long answer is no longer
  truncated: it is sent as follow-up messages split on a line boundary.
- Telegram answers are rendered from Markdown: bold, inline code, fenced code,
  headings and links are converted to the Bot API's HTML subset. If the API
  rejects the entities the message is resent as plain text, so a formatting bug
  cannot swallow a reply.
- A photo sent to the Telegram bot is downloaded and attached to the turn, so a
  vision model can see it; the caption is the instruction. The whole turn runs
  on the same runner and card as a text prompt.
- The opening screen follows a model or trust change. It was built once at
  startup, so an onboarding run that picked a model left the old name in the
  transcript next to a header showing the new one.
- A long word with no space to wrap at (a URL, a hash, a long identifier, or a
  bold or code span wider than the line) is now hard-split at the column
  budget, in display columns, so no rendered line is wider than the terminal.
  Left whole, it made the terminal wrap the line on its own and shift every row
  below, which is what made the tail of a long answer look scrambled.
- The transcript has a single event reader again. `Init` armed one and
  `startRun` armed a second, so after the first turn two goroutines read the same
  stream channel and raced, applying a turn's deltas out of order: the start of
  an answer read fine while the end was scrambled. The reader now starts once
  and each event re-arms it.
- A steer typed during a run is shown at once as a user block, separated from the
  block above by a blank line, instead of waiting for the agent's next step. The
  agent's later "Steering:" notice for the same text is dropped so it is not
  shown twice.
- Model and tool text no longer reach the terminal with its control characters
  intact. A raw escape sequence is a command the terminal obeys, so an SGR or a
  cursor move that a model printed bolded random words and rewrote the frame
  under the cursor, which looked like text cut and pasted from a neighbouring
  line even though the stored session was clean. Escape sequences are stripped
  (CSI, OSC and other escapes), a carriage return is dropped and a tab becomes a
  space before any styling runs, in the transcript and in the plain printer.
- Stopping a turn now stops the command it is running. `run_command` killed only
  the shell, so a grandchild such as `du` kept running and held the output pipe
  open; `Wait` never returned and `/stop` reported success while the turn stayed
  in progress. The command is now grouped with its children and the whole group
  is killed on cancel and on timeout.
- OAuth logins for xAI/Grok, ChatGPT/Codex, Claude, Google Antigravity and
  GitHub Copilot. A device code or a browser loopback callback stores a token in
  the secret file, refreshes it before the vendor ages it out and shows the
  provider as a subscription instead of a dollar rate.
- Antigravity signs in with Google's public installed-app client pair stamped
  into the binary at build time, from a git-ignored `.env.local` or the
  environment, so a release build needs no prompt and the pair never enters the
  repository. A build without the stamp asks for the pair once and stores it.
- The setup wizard routes an OAuth provider to a login step and says a login is
  required rather than asking for an API key, and `termixgo secret` refuses an
  OAuth provider.
- A browser OAuth login on a headless server: open the printed URL on another
  machine and paste the redirected `127.0.0.1` URL, with its code and state, back
  into the terminal to finish. An SSH tunnel still works where the port is fixed.
- `Ctrl+Y` and `/copy [last|all]` copy the newest answer or the whole transcript
  to the terminal clipboard with an OSC 52 escape, which needs no platform tool
  and works over SSH.
- Copilot models that answer "not accessible via the /chat/completions endpoint"
  are retried on the OpenAI Responses endpoint (`/responses`) and remembered, so
  models such as the MAI, Grok and some GPT entries work.
- `subagentModels` runs a delegated role (`explore`, `general`, `builder`,
  `code-review`, `security`) on its own model. `subagentFallbacks` is an ordered
  chain of providers a subagent falls through when one has no credential or runs
  out of quota; when set it is the complete set, so a subagent never lands on a
  provider the operator did not list.

- An MCP server's `env` (which can carry a token) is stored in the 0600 secret
  file instead of `config.json`, and a pair already in the config is migrated on
  the next start. This closes the Windows gap where `config.json` has no explicit
  owner-only ACL.
- The Telegram poll offset is persisted only after an update's handler finishes,
  with a contiguous watermark, so a crash replays an in-flight prompt instead of
  dropping it.
- Anthropic cache usage is counted once: the prompt total is
  `input + cache_read + cache_creation`, and pricing subtracts both cache parts
  before the regular input rate (cache read at 0.10x, cache write at 1.25x)
  instead of dropping the write or charging a cached token twice.
- `git_branch` passes a switch target after `--end-of-options`, so a name such
  as `-f` cannot force-switch and discard local changes from a read-only call.

### Fixed

- An OAuth login that the vendor has revoked no longer sends a dead bearer on
  every request. A refresh that answers `invalid_grant`, `refresh_token_reused`,
  `refresh_token_expired` or `refresh_token_invalidated` is now a `GrantError`,
  and `AccessToken` drops the stored credential when the access token is spent
  too, so `HasKey`, `/status` and the setup wizard all agree that a re-login is
  needed instead of failing the turn with an auth error. A temporary 5xx or a
  network failure still hands back the stored token.
- Concurrent refreshes make one token request per provider instead of racing.
  These vendors rotate the refresh token on use, so a second replay of the old
  one can invalidate the whole session; OpenAI logs the account out. `RefreshCodex`
  and its siblings now run behind a per-provider lock and re-check the store
  first, so a caller that queued behind the lock adopts the token the winner
  saved. Twelve concurrent callers now produce one refresh, measured in a test.
- Codex and Claude logins rotate before the vendor ages the grant out, not just
  when the hour-long access token expires. A spec can set `RefreshLead` (10
  minutes for Codex, 4 hours for Claude, matching 9router's `refreshLeadMs`) and
  `MaxRefreshAge` (8 days for Codex, its `maxRefreshAgeMs`), and a token records
  `LastRefresh` so a credential left unused for too long is renewed early.
- `/status` reports a provider's credential as an `oauth login` rather than
  showing "no key" for a provider that is logged in, and an API key stored under
  a login provider's id no longer impersonates a login there.
- `/cron run` no longer starts an empty turn for a job that does not exist. The
  not-found and read-failure branches used `break` inside the inner switch,
  which only left that switch: the command then printed `Running job .` and
  called the run path with an empty prompt. The branches are now cases of the
  same switch, so a refusal is the only thing that happens.
- Approval decisions keep being written to learned memory after a config change.
  `UpdateConfig` and `SetApprovalMode` replace the `ApprovalPolicy` to change the
  mode, and the replacement dropped its `Memory` field, so the runner silently
  stopped recording "the operator allowed this tool" after the first settings
  write of a session. The rewrite now keeps the memory and the session and
  always-allowed sets.
- `staticcheck ./...` is clean again, which the CI job requires. The findings
  were an error string that opened with a capital letter, the two unused
  functions `Runner.toolNames` and `Store.setProtectionErr`, a loop in
  `PruneCheckpoints` whose body always returned, an assignment in
  `decodeMultiEdits` whose value was overwritten before it was read, two
  redundant `break` statements, and four calls to the deprecated
  `viewport.HalfViewUp`/`HalfViewDown`. The Windows ACL helper now reads the
  process token through `windows.GetCurrentProcessToken` instead of the
  deprecated `OpenCurrentProcessToken`.
- Gemini token usage is no longer counted once per chunk. The Google endpoint
  repeats `usageMetadata` on every chunk with counters that cover the whole
  request, and the run loop sums every usage event to show the running total, so
  the same tokens were charged once per chunk: the context count and the
  estimated spend read several times too high and a `costBudgetUsd` tripped
  early. The client now emits the increase over what it has already reported, so
  the summed total equals the final cumulative figure. Measured on a three chunk
  answer whose real total was 10 prompt and 6 completion tokens, the old code
  summed 30 and 13.
- The screen no longer garbles a few turns into a session. A frame taller than
  the terminal made Bubble Tea drop its top lines and reposition the cursor
  relative to the previous frame, which shifts the whole screen: earlier lines
  appeared in the wrong place, which read as random and reversed text. Every
  list overlay is now windowed, the approval diff takes only the rows the
  terminal has left, and `View` clamps the frame as a last line of defence. The
  measurement that found it: at 100x30 the slash menu drew 46 rows and the
  approval dialog 36.
- A slash command typed during a turn is no longer handed to the model as prose.
  `/cost` and `/harness` reached the agent as the literal text "/cost", so the
  operator never saw the answer. Read-only commands and the live controls run at
  once; work that would collide, such as `/new`, is queued and runs as a command
  when the turn ends.
- `delete_file` with "." resolved to the workspace root and removed every file
  in it, then reported success. One confused call could take the whole project.
  It now refuses the root, as does `move_file`, which would otherwise take the
  session's working directory with it.
- `list_directory` with no path failed with a raw "Rel: can't make relative to"
  error. The workspace check rejects an empty path, and it ran before the
  fallback substituted the workspace root, so the documented default was dead.
- A finished background process no longer counts against the concurrency cap.
  The handle of a completed command is kept so its output stays readable, but
  counting every handle ever started made the manager refuse all further work
  once eight commands had finished, with nothing running left to stop.
- Every string bound now lands on a rune boundary. A byte slice at a fixed
  offset could cut a multi-byte character, and the terminal then paints a
  replacement glyph: the read window, the raw command output, a git log, the
  FTS5 snippet, a remembered fact, and an MCP server's stderr all had it. The
  Telegram body had it too, where the consequence is worse than a glyph: the
  Bot API rejects a body that is not valid UTF-8, so a message holding an emoji
  at the cut was never delivered at all.
- The token count and the session spend are no longer the first things dropped
  from the status line. That line is a single clipped row, and both figures sat
  behind the session id and the approval label, so a long task name pushed them
  off exactly while a run was busy. The vitals are placed last and kept; the
  spend is shown from the start, and an unpriced model reads "cost n/a" rather
  than a zero that looks like a free session.
- Typing `exit` or `quit` in plain mode now ends the REPL. The word was sent to
  the model as a prompt, which spent a call on it.
- A checkpoint no longer stashes the state directory. `.termixgo` was included
  as untracked content, so a later `stash apply` collided with the live search
  index and a rewind failed with "could not restore untracked files".
- A harness profile that caps the loop means that cap again. Segmenting the turn
  had quietly let the "shorter loop" profile run four times its stated budget.
- An approval or question dialog left pending when a turn ends is cleared, and
  the safe default is sent on its reply channel, so the agent goroutine waiting
  on it is never stuck.
- The error journal's compaction reports a failed write instead of silently
  leaving the journal truncated: the flush, the close and the rename all had
  their errors discarded.
- A Telegram answer with no text no longer sends an empty message, which the Bot
  API rejects.
- The workspace index refresh always clears its in-progress flag, so a panic
  during a walk cannot leave the index permanently refusing to refresh.
- A one-shot `termixgo run` releases the search index and the MCP servers on the
  way out. It opened them and never closed them, so the handles outlived the
  command.
- Tests that build an app register its shutdown, and three that inferred the
  working directory now move to a temp one first. Both were writing state, and
  on Windows an open index made a temp directory impossible to remove, so the
  test failed in its own cleanup.
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
- The transcript no longer swaps the answer and the reasoning. The closing
  reasoning event arrives after the whole stream, so by then the answer text is
  on top of the thinking block; matching only the last block appended a second,
  duplicated reasoning block below the answer, which read as the two being
  reversed.
- Wrapping now measures display columns, not runes. A wide glyph such as a CJK
  character or an emoji is one rune but two cells, so a line measured short,
  overflowed the terminal and wrapped, shifting every row below it. The
  assistant, user, reasoning and menu renderers all went through it.
- A menu or a dialog no longer grows the frame past the terminal. The slash and
  at-file menus, the approval dialog and the question dialog kept a full-height
  transcript and then drew themselves under it, so the frame was up to seven
  rows too tall on a common window; the clamp hid it by dropping the top rows,
  which moved the whole screen. The overlay now takes its rows from the
  transcript, a dialog is modal, and the help, setup and picker screens clip
  their text and bound their rows so the raw frame fits before the clamp.
- Model prices are corrected against each vendor's published rate table. The
  DeepSeek figures were the largest gap (the current standard cache-miss rate is
  $1.32/$3.96 for V4 Pro and $0.30/$1.20 for Flash, not the old figures), and
  Moonshot Kimi, Zhipu GLM, Alibaba Qwen, DeepInfra, SiliconFlow, Novita,
  Hugging Face, Vercel and Groq carried smaller drift. `TestVerifiedVendorRates`
  pins the figures read from each vendor page. Vendors whose pricing page is
  JavaScript-only (Baidu, Volcengine) keep their previous estimate.
- A plan subscription is no longer given a dollar rate. Qwen Cloud Token Plan
  and StepFun Step Plan are billed in credits, so their models report no dollar
  price, `/cost` says the plan bills in credits and that a `costBudgetUsd` cap
  does not apply, and the status line reads "plan Credits" instead of
  "cost n/a". Qwen publishes no per-model credit rate (the rate is dynamic), so
  none is invented; the Credit Pack gives one credit a $0.00075 marginal value.
- DeepSeek's current wire name is sent: `deepseek-v4.1-flash` now carries
  `APIID deepseek-flash`, which is the id the vendor documents, while the
  stable catalogue id is unchanged.
- `termixgo serve` reports why the assistant has no usable model instead of
  letting every Telegram message fail with a bare "no model is configured". A
  configured model id whose key or endpoint is missing is named, `/status` shows
  the problem, and the run error carries the cause. The CLI keeps its own
  settings, so a custom endpoint set in the desktop app is not shared with it.
- An unknown model id now prefers a configured OpenAI-compatible endpoint over
  a keyed vendor, so the endpoint's model no longer fails with a missing-key
  error.
- `web_fetch` no longer merges adjacent HTML blocks: a plain tag strip made
  `<h1>Guide</h1><p>Use it</p>` read as "GuideUse it". Blocks break into lines
  and entities, including numeric and named ones, are decoded. A DNS or
  connectivity failure is reported as offline with a hint not to retry.
- `web_search` now scrapes DuckDuckGo's HTML results page, which is the actual
  search, instead of the instant-answer API that returns nothing for a normal
  query. It falls back to the instant answer when the markup yields nothing, and
  a DNS failure names the host and tells the model to stop retrying.
- Two string bounds land on a rune boundary: the project memory that reaches the
  system prompt and a renamed session title. A fixed byte offset could cut a
  multi-byte character in half and put an invalid sequence in the prompt or the
  session file.
- The background-output ring buffer no longer cuts a UTF-8 rune when it trims to
  its cap, which surfaced as a replacement glyph in `run_logs`.

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
