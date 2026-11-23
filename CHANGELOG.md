# Changelog

All notable changes to Termixgo are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- **Antigravity through the agy CLI (experimental, on `feat/agy-backend`).** The
  `agy-cli-*` models run a turn through the installed Antigravity CLI, so the
  request to Google is the CLI's own. They appear usable only when agy is found
  (PATH, the standalone or IDE install directory, or `TERMIXGO_AGY_PATH`). It is a
  delegation: agy runs its own tools, and Termixgo's tools, approvals and trust
  rules do not apply to that turn. `TERMIXGO_AGY_MODE` picks `plan` (default),
  `accept-edits` or `auto`. The `antigravity-*` models keep working without the CLI.
- **Gemini 4 Neon on Antigravity**, with low, medium, high and xhigh thinking. Also
  GPT-6.2 Sol, GPT-6.2 Astra and GPT-6.1 Terra on Codex (GPT-5.6 and its siblings
  stay), Claude Haiku 5.5 on the Claude login, the Copilot models GitHub now lists
  (Claude Fable 5.2, Opus 5.5 and Haiku 5.5, GPT-6.1, GPT-6, Gemini 4, Grok 4.6,
  MAI Code 2) and Muse Ember 1.0.
- **Effort levels `xhigh` and `ultra`** beside low, medium, high and max. Each model
  gets the highest level it offers at or below the one chosen, taken from the
  vendors' own tables, so `ultra` reaches only the Codex models that list it.
- **Adaptive thinking and context management for Claude.** Models that take them
  (Fable, Opus 5, Sonnet 5, Haiku 5.5, Opus 4.7 and 4.8) now get
  `thinking: {type: adaptive}` with the reasoning summarized into the transcript, and
  `context_management` with Claude Code's own `clear_thinking` edit that keeps earlier
  thinking in context. The signed thinking blocks of a step are stored and handed
  back in front of its tool call, as the API requires. A 400 about thinking repeats
  the request once as a plain one, and `TERMIXGO_CLAUDE_THINKING=off` turns the
  parameter off.
- **Muse usage meter.** Meta ends every response with a subscription usage event
  (5-hour and weekly windows). Termixgo reads it, so the Muse row above the composer
  moves with each turn, and an idle session probes once with a tiny request.

### Changed

- **Antigravity requests match agy 1.5.14.** The User-Agent carries the CLI's
  `aidev_client` fields and changelist, account calls send only the IDE type, the
  chat call adds `requestType`, `labels` and upper-case thinking levels, and the
  session, client-name and `toolConfig` fields the CLI never sends are gone.
- **Codex requests match the Codex CLI**: hyphenated `Session-Id` and `Thread-Id`,
  turn metadata, a CLI-shaped User-Agent, the responses-lite header for the GPT-6
  line, and the context windows the ChatGPT catalogue lists.
- **Claude Code identity moved to 2.1.325**: the current beta list, the billing
  header with its fingerprint, and the interactive `(external, cli)` User-Agent.
- **Antigravity signs in with the agy CLI's own OAuth client** instead of the IDE's.
  The pair is read from the installed binary into the git-ignored `.env.local`; log
  in again to get a token issued to it.
- **Copilot** reports the installed VS Code 1.147.0 and Copilot Chat 0.75.0, and Muse
  reports CLI 1.4.24.

## 0.2.11 - 2026-11-23

### Added

- **Audit and tax tools.** `calc` does exact decimal arithmetic (expressions, or a
  file column), `reconcile` matches two tables by key or amount, `benford_test`
  screens a column of amounts, and `evidence_log` keeps a hash-chained working
  paper whose `verify` catches an edited entry or a source file that changed. The
  tools carry no tax rates: the prompt now tells the agent to take every rate,
  threshold, deadline and standard from the current official regulation, check
  its effective date and cite it, and to say so when it cannot confirm the rule.
- **Research tools.** `web_extract` returns an article's metadata and main text,
  `cite` keeps sources and exports APA, BibTeX or markdown without inventing a
  missing author or date, and `wayback` finds, lists or saves Internet Archive
  snapshots (never of a private address).

### Fixed

- **Web tools no longer fail on a bad DNS answer.** A name the system resolver
  hands back with a wrong address (an ISP block page or a poisoned cache) used to
  end in a certificate error or a reset, because only a failed lookup fell back
  to DNS-over-HTTPS. A GET that fails that way is now retried once with the
  address taken from DoH. DoH itself asks four resolvers in turn, two addressed
  by IP so a blocked resolver name cannot stop it, reads IPv6 when a host has no
  IPv4, stops at a name that does not exist, and remembers an answer for its TTL
  (30 seconds to 10 minutes). All web tools share this: search, fetch, extract,
  wayback and the browser snapshot. The model providers and the Telegram bridge
  use the same fallback (shared in `internal/dohnet`); a request with a body is
  repeated only after a handshake or connect failure, so a message is never sent
  twice.
- A finished plan no longer follows you into the next task. Once every item is
  completed the plan is cleared from the live store and the saved session (the
  transcript keeps the final list), so the status line stops saying `plan 4/4`
  and the live view stops showing the old list. A plan that finishes during a
  run leaves the live view and the status line at once. An unfinished plan is
  kept.

## 0.2.10 - 2026-11-22

### Added
- A usage strip above the composer shows what is left of the 5-hour and weekly windows of every logged-in provider (Claude, Codex, Antigravity, Copilot, Muse, Grok), not only the active one: a bar and a percent that both mean left, coloured by how much remains, with the reset countdown. It replaces the gauge at the right of the header and the local `5h:` count in the status line, uses the providers' own figures only (`...` until they arrive, `n/a` when a provider has no usage endpoint), refreshes in the background at most every three minutes per provider, and drops detail on a narrow terminal instead of wrapping.

### Fixed
- A background subagent, monitor or scheduled prompt could finish without the model ever hearing of it: wake events shared the display buffer, which drops events when it is full, and a stream of text deltas fills it. A wake now waits for room (bounded) instead of being dropped, and the plain, non-terminal mode prints it.
- `run_monitor` is refused in a chat turn, where its notification would land in the terminal's event stream; at most 8 monitors and 4 background subagents run at once; the wake text tells the model that quoted process output is data, not instructions.
- `run_command` reported `Command succeeded in .`, which read as a missing duration: the text names the directory, and the workspace root is now spelled out.

### Changed
- The README screenshot is replaced by a set of thirteen taken from the real terminal UI: first-run setup, the Claude OAuth login, choosing a model and a command, an edit and a command waiting for approval, a run in progress, a finished turn, and plan mode. `scripts/screenshots/` regenerates them (the real binary in tmux against a scripted model), and the broken image link in the README is fixed.
- `termixgo service install` on Linux writes a unit with `Restart=always` and no start-rate limit, so the assistant is restarted after a crash loop or a clean exit instead of staying down.

## 0.2.9 - 2026-11-22

### Added
- `submit_plan`, the plan-mode handshake: the agent presents its plan, the TUI shows it with a "Ready to code?" dialog (start with edits accepted automatically, start with each change confirmed, or keep planning with feedback), and an approval switches the same turn out of plan mode and is saved. Chat channels review the plan as a question in that chat, and their approval affects only that turn.
- `web_fetch` takes a `prompt`: the page is read by the internal task model and only the answer comes back, recorded in the session ledger under that model; a failed extraction returns the page with a note.
- Inline TUI: one activity line (`✻ Thinking… (14s · ↓ 2.1k tokens · esc to interrupt)`), a smooth theme-aware shimmer on its own animation tick, answers printed paragraph by paragraph as they stream, and one visual language (`●` answers and tools, `⎿` output, `∴` reasoning).
- Background and resumable subagents: `run_subagent` takes `background`, every subagent keeps its session and id, `send_subagent` continues it with its full context, and `subagent_status` lists, waits for, reads or stops one. A background run's spend is counted in the turn that reads its report.
- `run_monitor` watches a background process and wakes the model when an output line matches a regular expression and when the process exits; matches are batched and spaced, and capped per monitor.
- Wakes: a finished background subagent or a monitor match starts a turn when the terminal is idle (shown as a `Background:` line) and is folded in as a steer while a turn runs. At most five turns start in a row without the operator typing.
- `schedule_task`: the model schedules a prompt for later (cron expression, `every 10m`, `at 15:30`); it fires as a wake, recurring tasks expire after 7 days and repeat at most once a minute. `durable` stores a cron job run by `termixgo serve` instead.
- `notebook_read` and `notebook_edit` for Jupyter notebooks: cells by index and id, replace, insert or delete, unknown fields kept, stale outputs cleared.
- `ask_user` takes `questions`, up to four asked in one call, answered in order.

### Fixed
- Claude OAuth models did not appear after logging in: `termixgo login claude` (or `claude-code`, `github`) stored the token under the alias typed, which the provider never reads, and a terminal already running did not see a login made in another terminal until it restarted. Logins are stored under the provider's own id, a token saved under an old alias is still found, and a running terminal picks up a new login without a restart.
- Todo items stayed unchecked: the plan the model reads showed no ids while `todo_update` matched ids only, so the model guessed and missed; `todo_update` was not offered when tool search was on; and starting the next item while one was in progress failed the whole update. The plan now shows ids, `todo_update` matches an id in any case, a number or a title and takes `ids` for several items, starting an item marks the active one completed (with a note), and the critic names the ids and the call to make.
- A Telegram or WhatsApp turn started with an empty plan and never saved it, so a chat could not tick off an item from an earlier turn, and `/plan` in a chat fell back to the terminal's plan. Each chat now keeps its own plan.
- Windows notifications never appeared (unregistered app id) and broke on `<` or `&`; the resize reprint's scrollback erase raced the renderer.
- The guest deny-list named five tools by old names (`logs`, `wait`, `list_processes`, `kill`, `subagent`), so a chat guest could read and kill background processes and spawn subagents. It now uses the real names and also covers SSH, `dev_server`, `env_list`, `browser_run_steps`, scheduling and the new subagent tools; a test fails if a listed name stops matching a tool.
- Guest steer messages on Telegram and WhatsApp reach the running turn wrapped as untrusted data, like the guest's prompt.
- With tool search on, `run_subagent` was deferred because the always-on list named it `subagent`.
- Telegram streaming: a near-limit draft could be edited over the message limit, a silent answer after overflow left the live draft in the chat, and the card's dropped-lines count reset on every update.
- Reasoning closes with its own duration as soon as the answer or a tool starts, instead of holding the answer in the live area until the turn ends.
- Truncated text in `read_document`, media requests and eval output is cut on a character boundary, never inside a multi-byte character.
- The context gauge counts only the tool schemas a request carries; after any width change the inline transcript is cleared and printed again at the new width.

## 0.2.8 - 2026-11-21

### Added
- Claude Code-grade TUI productivity and ergonomics:
  - Persistent `Up`/`Down` prompt history plus `Ctrl+R` reverse-i-search overlay across saved prompts (`Enter` run, `Tab` edit, `Ctrl+R` next match, `Esc` restore draft).
  - Full-screen scrollable transcript viewer via `Ctrl+O` (in inline mode) or `/transcript` (in any layout), displaying full reasoning and tool details plus `/` line filtering.
  - `Shift+Tab` approval mode cycling (`ask`, `edits`, `all`, `plan`) directly from the composer.
  - Double-`Esc` on an empty composer to edit and resend or rewind the last turn (`Session.PopLastTurn`, `App.RewindLastTurn`), with optional git checkpoint restore.
  - Per-turn completion summary (`steps`, input tokens with prompt-cache hit percentage, output tokens, estimated cost, elapsed time) and non-blocking OS desktop toast notifications + terminal bell for long turns and waiting approvals.
  - `/context` token breakdown, `/permissions` allowlist manager, `/rename` session rename, and `/layout [inline|fullscreen]` runtime layout toggle.
- OpenClaw/Hermes-style Telegram live draft streaming (`internal/telegram/stream.go`): progressive draft delivery via `sendMessageDraft` (with automatic `sendMessage`/`editMessageText` fallback), live reasoning/tool activity indicator, multi-chunk overflow flushing across 4096-char limits, and English approval/model-picker cards.
- Language server tools: `lsp_definition`, `lsp_references`, `lsp_hover`, `lsp_symbols`, `lsp_diagnostics` and `lsp_rename`, over gopls, typescript-language-server, pyright, rust-analyzer, clangd and others found on PATH (`lspServers` overrides per extension). A crashed server is started again on the next call.
- `generate_image` (OpenAI gpt-image-1 and DALL-E, Gemini image models, Imagen, xAI) and `text_to_speech` (OpenAI, Gemini TTS, then the operating system voice). Files land in `.termixgo/media`; guests cannot use either.
- TUI image paste: Ctrl+V or Alt+V attaches the clipboard image as `[Image #N]` at the cursor, an image file dragged onto the terminal is attached instead of typed, and deleting a placeholder drops its image.
- `convert_document` converts Word, Excel, PowerPoint and OpenDocument files (to PDF, modern Office formats, CSV and more) through LibreOffice, or Microsoft Office over COM on Windows; the Office instance it starts is always closed. `create_office_document` writes multi-sheet workbooks with numeric cells, a bold frozen header and fitted columns.
- `browser_run_steps` runs a multi-step browser flow (navigate, click, type, press, wait, scroll, read, screenshot) in one call, stops at the first failing step, saves flows with `save_as` and replays them with `{{name}}` variables. Typed values are never echoed or saved. `browser_fill_form` now points here instead of at `browser_click`.
- `/endpoint list|add|rm|models` manages several OpenAI-compatible servers from the TUI; `termixgo endpoint models <name>` fetches a server's model list from `GET /models` (OpenAI and Ollama shapes).

### Changed
- The TUI runs inline by default, as Claude Code does: finished messages are printed once into the terminal's own scrollback and only the turn in flight, the status line and the composer are redrawn, with no alternate screen and no mouse capture, so the terminal's scrolling, selection and copy work. `"tuiLayout": "fullscreen"` or `TERMIXGO_FULLSCREEN=1` restores the previous full-screen layout. Consecutive finished read/search tool calls collapse into a single summary line in compact mode, and the active plan stays pinned above the status line during inline runs.
- TUI keys follow Claude Code: Esc closes a menu, interrupts a turn, clears a draft on a second press, or opens the rewind/edit menu on an empty composer; Ctrl+C copies a selection, else interrupts or clears the draft, and exits on a second press; Ctrl+X asks before quitting with a run or a draft.
- The composer grows with its draft from 3 to 12 rows (at most a third of the window).
- Long pastes collapse to `[Pasted text #N +X lines]`; the turn receives the whole text.
- The transcript starts compact: commands and failed tools show 3 output lines under the tool line with `… +N lines (ctrl+o to expand)`, thinking collapses to `Thought for Ns`; Ctrl+O shows 40 output lines and the full reasoning instead of 8 trimmed lines.
- Markdown in the TUI renders tables, quotes, rules, nested lists, links and `~~~` fences, and wraps long code lines instead of clipping them.
- The context gauge uses the provider's reported prompt tokens once a step has run (an estimate is marked `ctx: ~`), and no longer repeats the model name.
- A resumed session shows each tool call's label, real outcome and output preview.
- Enriched channel markdown across Telegram and WhatsApp (bold, italic, bold-italic, strikethrough, quotes, rules, nested lists, links, tables, and safe code spans).
- grep has `output_mode` `files` and `count`, groups content under one path line per file and takes `context` lines; grep and glob honour `.gitignore` through `git ls-files` and skip lockfiles, minified bundles and source maps unless `include_noise` is set.
- The learned-memory block in the prompt keeps the newest facts within 6 KB across scopes and leaves out the approval bookkeeping lines.

### Fixed
- WhatsApp and channel anti-repetition guard (`internal/agent/repetition.go`): detects and aborts degenerative streaming loops mid-turn, nudges the model to run tools instead of repeating "checking..." promises, and collapses stuttering prose on final channel delivery.
- Telegram & WhatsApp channel audit fixes: fixed document caption truncation/overflow, HTML tag nesting in headings/links, multipart upload handling, immediate `/stop` cancellation while approvals are pending, non-admin approval rejection, and `search_memory`/`web_search`/`web_fetch`/`lookup` availability in always-on channel tools.
- The prompt cache prefix stays stable: the plan is no longer part of the system prompt, the tool search set persists for the session and only grows at its end, and the Anthropic moving breakpoint sits on the last message. OpenAI and Codex requests carry a `prompt_cache_key`, and the status line shows the cache hit rate.
- A launch starts a fresh session instead of silently loading the newest saved one, so the model no longer resumes another project's pending work; switching agents only resumes a session from that agent's workspace, and `/new` clears the old transcript.
- `send_channel` asks before sending a file from outside the workspace or a message to anyone but the caller.
- Escape in the approval and question dialogs is held like the chat Escape, so a mouse scroll no longer denies an approval; `!cmd` no longer freezes the UI; the idle screen stops repainting ten times a second; Ctrl+T now hides the reasoning body.
- A crashed browser or MCP server is replaced instead of failing every later call; an MCP call interrupted by the crash is reported, not repeated.
- Telegram keeps code spans and link URLs intact; chunked messages close and reopen code fences.
- With several named endpoints, an unlisted model resolves to the same endpoint every time instead of a random one.

## 0.2.7 - 2026-11-16

### Fixed
- Per-step token waste: turns now defer to tool search once the registry passes 40 tools instead of 128, so the default 122-tool registry no longer resends about 23.5k tokens of tool schemas on every step (the deferred always-on set measures about 6.8k). Subagent runs apply the same rule instead of inheriting only the config flag. One `read_file` result is capped at 32 KB instead of 64 KB, because a tool result stays in the history every later step resends; `offset` and `limit` page through the rest, and the clipped marker is derived from the cap instead of a hardcoded size.
- Muse OAuth identity now presents the official `muse-build/1.4.20` client string for the host platform, shared between the key mint and the provider through `internal/museidentity`.
- Codex and Muse Responses streams keep tool names attached to decoded calls and tolerate reasoning items issued by another backend without encrypted content.

## 0.2.6 - 2026-11-15

### Added
- Antigravity Gemini 4 models (`antigravity-gemini-4-hydrogen`, `antigravity-gemini-4-argon`, `antigravity-gemini-4-carbon`) and `antigravity-gemini-3.8-flash-high` in the model catalogue with 1M context windows, effort-mapped wire IDs (`gemini-4-hydrogen-{low,medium,high,xhigh}`, `gemini-4-{argon,carbon}-{low,medium,high}`), and automatic one-shot fallback to bare `gemini-4-*` IDs on 400/404.
- Claude OAuth auto-refresh on 401 and automatic `claude_code_version_too_old` version learning and retry on 400.
- StepFun Plan model `step-5-preview` (`stepfun-plan/step-5-preview`) in the model catalogue, selectable through `/model` and the picker like the other plan models.
- Multiple named OpenAI-compatible endpoints: `termixgo endpoint add <name> <url>` and `termixgo endpoint rm <name>`. Each is an `openai-compatible/<name>` provider with its own URL, key and models, so registering a new endpoint no longer overwrites the previous one.
- `muse-wonder-1.0` and its contributor variant are in the Muse catalogue, with a 1M-token window.
- `muse` is a `code_worker` kind: the task runs through the official Muse Code CLI (`muse exec`) in its own worktree, so a Muse Code subscription covers tool use even though the raw API no longer allows it from another client.
- A Meta Model API key (`LLM_...`) now takes precedence over the Muse OAuth subscription login, whether stored with `termixgo secret muse` or exported as `META_API_KEY`. That key is the pay-as-you-go credential Meta documents for third-party clients and may use tools.
- Agent artifact tools (`artifact_write`, `artifact_read`, `artifact_list`) keep large tool output in a size-capped in-memory store and read it back by id or line range; `env_list` lists environment variables while redacting sensitive names and stored-secret values.
- A `dev_server` tool starts a development server, waits for it to become ready by probing the URL from its logs, and reports the process handle so later steps can read its output.
- SSH tools (`ssh_list_connections`, `ssh_connect`, `ssh_run_command`, `ssh_active_session`) manage saved connections and run commands on remote hosts. Connection passwords are held only in memory (`json:"-"`) and never written to the `0600` connections file; `golang.org/x/crypto` moves from indirect to direct for the SSH client.
- `read_pdf` extracts text via `pdftotext` (poppler) with a Python `pypdf`/`PyPDF2`/`pdfminer` fallback; `list_policies`, `pin_invariant` and `list_invariants` manage operator guardrails in a `0600` `~/.termixgo/policies.txt`; `todo_update` updates one todo item (status or title) in place through the in-memory `TodoStore`.

### Fixed
- Claude Code OAuth requests now place `"You are Claude Code, Anthropic's official CLI for Claude."` as the first standalone `system` block (preventing `429 rate_limit_error` refusals), send `X-Claude-Code-Session-Id` and host-accurate `X-Stainless-Os`/`X-Stainless-Arch` headers, use the dynamic CLI User-Agent on `/api/oauth/usage`, and trigger the manual pasted-code fallback when port `54545` is busy even for `claude` and `claude-code` aliases.
- Antigravity requests, quota fetches, and OAuth token exchanges now present as `antigravity/cli/1.5.9 <os>/<arch>` (instead of `antigravity/ide/2.11.0 darwin/arm64`) with `X-Client-Version: 1.5.9`, matching Termigo and ODON so Gemini 4 Hydrogen, Argon, and Carbon are served by Cloud Code.
- History compaction now preserves compact tool-call arguments (`<= 300` bytes) and clips only oversized string values instead of overwriting `ToolCall.Arguments` with `"{}"`, and `LoadSessionIn`/`Compact` automatically repair legacy elided `"{}"` tool calls in saved sessions into schema-valid placeholders so models are never poisoned into calling `read_file`, `grep`, `glob`, or `code_outline` with empty arguments.
- OpenAI-compatible streaming and whole-body decoders now accept raw JSON objects on `function.arguments`, `parameters`, and `input` deltas and split tool calls when a gateway reuses `index: 0` across multiple tool calls in a single response.
- `BaseName("")` now returns `""` instead of `"."` so failed `read_file` calls with an empty path no longer render as `x Read .`, tool parameter aliases are expanded across file/search/outline tools, and scratch edits under `.termixgo/` or `.termigo/` no longer trigger false-positive `verify-on-stop` nudges.
- History compaction now advances by complete user-initiated exchanges up to the next user turn rather than dropping message by message. Dropping individual messages previously split assistant tool calls from their opening user prompts, leaving orphaned tool calls at the head of conversation history which Gemini and Antigravity reject with HTTP 400 INVALID_ARGUMENT.
- Tool argument decoding in both agent and provider is now hardened against common LLM formatting variations, automatically repairing trailing commas before closing braces/brackets, converting unquoted Python-style boolean and null literals (True, False, None) to standard JSON, and parsing single-quoted JSON objects.
- Tool progress rendering across Bubble Tea UI, plain/headless output, and channel bots now enforces fallback labels (Running <tool>..., Ran <tool>, Failed <tool>) whenever tool definitions return blank labels, eliminating blank >  markers.
- WhatsApp bot now connects a progress observer into RunPrompt and RunPromptWithImage to renew composing presence on every progress and tool event.
- Antigravity turns with tool calls failed with a 400 "Please ensure that function call turn comes immediately after a user turn or after a function response turn". The encoder emitted one `user` turn per tool result, so a step that called three tools sent three function-response turns and left the second and third answers standing behind an answer the backend had already consumed. Consecutive results are now folded into the single `user` turn that answers the call, a turn that encodes to no parts keeps a placeholder part instead of vanishing (dropping a user turn from between two model turns breaks the same rule from the other side), an empty tool result is sent as `(no output)`, and a functionCall that history leaves standing first or behind another model turn gets a continuation turn in front of it rather than being dropped.
- A Muse Code subscription login could not chat at all: login and the key mint succeeded, but every Termixgo turn carries tools, and Meta returns `403 client_attestation_denied` for any request that registers tools from a non-official client. The Muse client now switches (once, sticky per client) to a text tool protocol when Meta refuses the native tools field: the tools are listed in the instructions, the model replies with `tool_call` blocks, Termixgo executes the parsed calls locally under the same approval gates, and stored calls and results are rendered back as `tool_call`/`tool_result` blocks, so the subscription login drives the tool and exec loop. A pay-as-you-go Model API key is never refused and keeps native tool calls, and the login output says which mode a credential will use. Only a refusal of the tool-less request itself remains a terminal policy error, and the generic "check the API key" hint no longer blames a valid key for it.
- Meta Muse Code now enforces client attestation for tool use: a Muse Code subscription key still answers a plain chat request, but any request carrying tools returns `403 client_attestation_denied`. Termixgo reports this as a policy refusal naming the pay-as-you-go key at https://dev.meta.ai as the next step, instead of a misleading key error. The request identity also matches Termigo again (`User-Agent: muse-code/1.0.2`, no `X-Client-Id` or `x-api-version`), and `providerStatusError` keeps the provider's error code so a policy refusal is matched without prose.
- The Linux installer verifies the Antigravity client id and secret are embedded after a source build, warns when `.env.local` is missing either value, and prints a confirmation when both are stamped, so a build cannot silently ship without the OAuth pair.
- Turns now switch to tool search automatically when the registry exceeds 128 tools. Five configured MCP servers contributed 408 schemas, and `codex-gpt-reserve` and `codex-auto-review` answered such a request with an empty response; with the core set plus `find_tools` both models reply and can still call tools. The cap is also OpenAI's documented function-calling limit, so it keeps every step under it.
- Codex requests now send `OpenAI-Beta: responses=experimental`, the header the working Termigo client sends on every ChatGPT request. It aligns the client with the reference; it was not the cause of the empty responses.
- Codex/ChatGPT OAuth runs that were cut at the backend output limit with nothing usable no longer retry the identical request three times and then report a misleading idle run. A cut with zero output tokens and no item stops at once with an error naming the model (an oversized request is one cause); a cut that spent output but returned nothing visible retries once with the effort inside the reasoning object and no auto summary or encrypted-content include (this backend rejects the older top-level `reasoning_effort`) plus a short-answer steer; a second one stops with an error naming the cause and the next action. Both stops exit non-zero.

## 0.2.4 - 2026-10-31

### Added
- Optional container sandbox (`sandbox` in config) for shell execution: `run_command`, `run_checks` and the hypothesis check run inside Docker or Podman with the workspace mounted at the same path, no inherited environment, no Linux capabilities, `no-new-privileges`, a pid limit, the operator's uid/gid on POSIX, and `none` network by default. `run_background` is refused while it is on, a missing engine or image refuses the command instead of falling back to the host, and `doctor` reports whether the engine is reachable.
- Durable turns: a saved conversation marks a turn in flight and flushes it after the prompt, after each model step and after each tool batch. If the process dies, the next load closes every unanswered tool call with a result that says its effect is unknown, so the transcript stays valid for the next model call and a call that may already have happened is never silently repeated. The terminal reports the recovery once.
- Streamable HTTP transport for MCP servers: a server configured with `url` (and optionally `transport`) is reached over HTTP instead of a local process. The session id the server issues rides on every request, a 404 triggers one re-handshake and one retry, an answer is taken only from the SSE event whose request id matches, and `headers` are moved into the secret store exactly like `env`. stdio stays the default when only a command is set.
- Health alerts under `serve` (`healthAlerts` in config): every interval the daemon reads disk, memory, swap, failed systemd units and OOM kills, and sends a chat message when a threshold is crossed. Each condition has a cooldown, the first check runs 30 seconds after start, `/status` shows the last result including a delivery failure, and a host with no swap or memory figure is skipped instead of read as 0 or 100 percent.
- Local HTTP API (`termixgo api on`, `internal/api`): `POST /v1/turn` runs one prompt on a throwaway session and returns the answer, with `GET /v1/health`, `GET /v1/tools` and an unauthenticated `GET /healthz`. It binds `127.0.0.1:8787` by default, requires the bearer token stored under `api:token` in the secret store, warns when bound beyond loopback, caps bodies at 1 MB and turns at 60 per minute, and enters the run queue at channel priority.
- Priority turn queue: the operator outranks a channel message, a channel message outranks a scheduled job, and a job outranks the heartbeat. A second caller now waits for its bound instead of failing at once (`app.ErrBusy` still answers a heartbeat and a timed-out wait), so a background self-check or cron run no longer bounces the person at the terminal. `/status` reports the slot and how many callers are waiting.
- Bounded JSONL logs (`internal/rotate`): `audit.jsonl`, each session's usage ledger and `cron-history.jsonl` rotate at 8 MB and keep three archives, read back oldest first by `/audit`, `/cost` and `cron runs`, so a daemon running for months cannot fill a small disk.
- Live browser over the DevTools Protocol (`internal/browser`): `browser_navigate`, `browser_read` (text, HTML, links, form controls), `browser_click`, `browser_type`, `browser_press`, `browser_scroll`, `browser_wait_for`, `browser_eval`, `browser_tabs`, `browser_cookies` and `browser_close`. One headless Chromium stays open between calls with its cookies, so a login or a multi-step form works end to end. `browser_screenshot` now captures the live page, full page or a single element when a session is open, and falls back to the one-shot headless capture otherwise. The browser closes after five idle minutes and on a clean exit.
- Terminal attachments: `/attach <path...>` puts images and documents on the next turn (`/paste` reads the real clipboard, `/detach` clears the set) and the composer shows what is pending. Images reach a vision model through `App.RunTurnWithImages`; PDF, .docx, .xlsx, .pptx, .epub and HTML are extracted to text by the new `document_extract` tool. A paste over 100 KB is spooled to `.termixgo/pastes/` instead of truncating at the old 32,000-character composer limit.
- Local desktop control behind `desktopControl` (default off) and `/desktop [on|off|status]`: `desktop_screenshot`, `desktop_input` (move, click, drag, scroll, type, key) and `desktop_windows` (list, focus, launch). The tools use the host's own drivers, are approval gated, and are removed from the registry for a guest chat.
- WhatsApp in-chat approvals: an admin answers a tool approval by replying in the chat (y once, s session, a always, n deny), so mutating tools work over WhatsApp on a headless machine, not only from the terminal.
- Native Channel Hook interceptor for WhatsApp and Telegram (`channel.InvokeInboundHook`, `channel.PostHTTPHook`): intercepts inbound channel messages before LLM execution via REST API (`http://localhost:xxxx/api/bot/handle`) or in-process Go callback (`channel.MessageInterceptor`), supporting direct replies, prompt rewriting, message dropping, and post-turn completion notifications.
- CLI command `termixgo channel hook <whatsapp|telegram> [agent] [--url <url>] [--secret <secret>] [--test] [--clear]` to view, configure, test, or clear REST interceptor endpoints per account.
- Agent-scoped learned memory alongside project and global memory, indexed for retrieval without leaking facts between agents.
- Live quota gauges for Claude OAuth, Qwen Token Plan, and StepFun Plan, including reset-time context when providers expose it.
- Single-instance channel locks that prevent duplicate Telegram and WhatsApp pollers across foreground and background processes.

### Changed
- Setup now names the current stage, command discovery is explicit on the welcome screen, transcript errors carry a scannable label, and narrow terminals retain a compact diff summary.
- WhatsApp marks inbound messages as read and publishes composing presence while a reply is being generated.
- Release builds now fail closed when Antigravity OAuth credentials are absent and verify both stamps in every distributed binary.

### Fixed
- xAI OAuth and StepFun Plan now request streamed usage from their OpenAI-compatible APIs, so server-reported token usage and rate-limit headers reach the quota gauge instead of silently falling back to estimates.
- A cut-off Codex response no longer kills the run: when the Responses backend ends a response incomplete (`max_output_tokens`, a content filter) after the step produced anything, the partial answer, tool call or reasoning items stand, a notice names the reason, and the run continues; only a cut-off that produced nothing errors.
- An "Always" approval answer over a channel now persists the tool-level allow the way the terminal does, so the same tool stops asking on the next turn.

## 0.2.3 - 2026-10-29

### Added
- Cross-channel message and attachment delivery (`send_channel` tool): enables the agent from the terminal UI, WhatsApp, or Telegram to send text messages, images, and document files across any configured WhatsApp or Telegram channel (`whatsapp:default:628xxx`, `telegram:default:123456`).
- Outbound WhatsApp and Telegram media APIs: native `SendImage` and `SendDocument` over `whatsmeow`, and `SendPhoto` and `SendDocument` multipart uploads on Telegram Bot API.
- Voice note transcription and media model configuration: `transcribe_audio` tool for voice messages alongside `termixgo model voice` (`voiceModel`) and `termixgo model image` (`imageModel`) CLI and TUI `/model` commands.
- Multi-layer prompt injection and Base64 payload defense (`channel.SanitizeUntrusted`): detects and neutralizes single-layer and double-encoded Base64 shell scripts (`#!/bin/`, `curl | sh`, `eval`, `powershell -enc`, `os.system`, `child_process`) and Base64-encoded prompt-injection instructions before they reach the model context.
- Untrusted XML boundary protection: escapes `<untrusted_channel_message>` and `</untrusted_channel_message>` breakout tags in inbound guest messages.
- Non-admin attachment gate: blocks non-admin users in open DMs or group chats from uploading files, scripts, images, or voice notes before any media payload is downloaded to disk.
- Outbound Base64 secret redaction: extends `channel.RedactSecrets` to scrub standard, raw, and URL-safe Base64 encodings of stored secrets and API keys from outbound replies.
- Tool call resilience: automatic parameter alias normalization for `lookup`, `web_search`, and `web_fetch`, a `find_tools` discovery tool when tool search mode is active, and a search-engine URL guard in `web_fetch`.

### Fixed
- Antigravity OAuth token refresh persistence: `refreshTokenOnce` now persists link-time stamped client credentials to `secrets.json` (`oauth:antigravity:client`) during token refresh, preventing 401 errors when refreshing tokens on headless servers.
- Build and installer OAuth stamping: aligned `Makefile` (`build` and `cross` targets) and `installer/install.sh` with `build-cli.mjs` so local and cross-compiled builds consistently stamp `AntigravityClientID` and `AntigravityClientSecret` from `.env.local`.
- Staticcheck ST1005 compliance in Google stream error formatting (`internal/provider/google.go`).

## 0.2.2 - 2026-10-28

### Added
- WhatsApp enriched markdown formatting: standard markdown bold (*text*) without double asterisks, bold-italic (_*text*_), headings as bold, clean bullet dots, and link formatting.
- WhatsApp image and PDF document support: parses and downloads attachments via whatsmeow, attaches vision multimodal input for vision-enabled models (Gemini, Antigravity, Claude), and saves documents to the workspace uploads directory for tool-based inspection.
- Telegram enriched markdown: support for strikethrough (<s>), bold-italic (<b><i>), italic (<i>), blockquotes (<blockquote>), bullet dots, and syntax-highlighted code blocks (<pre><code class="language-...">).
- Telegram PDF document handling: routes PDF attachments directly to vision for multimodal models and saves to the workspace uploads directory.
- Anthropic document blocks: mapped MIME type application/pdf to the native Anthropic document block type.

### Fixed
- WhatsApp channel reliability: replies now use a detached background timeout context so error notifications are never dropped silently when an upstream turn times out or cancels.
- Clear error guidance on expired provider OAuth credentials, prompting the operator or user to switch models or renew credentials.
- Verified automatic Google OAuth token refresh in Antigravity provider using link-time stamped client credentials.

## 0.2.1 - 2026-10-28

### Added
- Bilingual slash commands in WhatsApp and Telegram: automatically adapts output language based on sender country code (+62 for Indonesian, default to English for international users).
- Primary active model filtering in /models: only displays providers with active registered credentials or configured endpoints, filtering out redundant legacy model sub-variants.
- Explicit agent SOUL.md and USER.md identity framing: prevents the agent from searching workspace directories for identity files or reading legacy configuration files from other software.
- Identity and conversational exclusion in strict grounding: prevents introducing oneself from triggering unnecessary tool evidence demands or unverified notes.
- Preservation of operator-authored SOUL.md and USER.md during installations and upgrades.

- Named agents: `agents.<name>` personas with `SOUL.md`/`USER.md`, their own
  workspace, sessions, memory, search index and skills, a `/agent` switcher and
  the `termixgo agent` command group.
- Channel registry: per-account Telegram and WhatsApp configuration with
  `dmPolicy`, `groupPolicy`, allowlists, admins, guest limits, per-chat prompt
  overrides and a pairing store (`termixgo channel ...`).
- WhatsApp channel on whatsmeow: QR or pairing-code login, one device store per
  account, mention-only group replies, per-chat sessions and natural text
  answers with typing presence.
- Guest guardrails: an admin-only tool class removed from the guest registry,
  no secret store, forced approval, rate/step/cost limits and polite refusals.
- Anti-hallucination: an `## EVIDENCE` prompt block, `grounding: strict` with a
  single evidence retry and an unverified marker, outbound secret redaction and
  untrusted-input wrapping.
- Delegation limits: per-agent `maxChildren`/`maxDepth`/`autoApprove`, context
  inheritance and operator approval for guest delegation.
- Multi-agent cron: jobs carry an agent and a delivery target
  (`channel:account:chat`, `local`), with `cron runs` and `cron status`.
- `doctor` now reports agents, channel accounts, linked state and a warning when
  an account reaching non-operators is bound to the launch directory.
- `health` command: system health and observability check (`termixgo health`)
  reporting disk capacity, memory, swap pressure, load averages, open file
  descriptors, systemd failed units, and kernel OOM kills.
- `clean-snapshots` command: workspace snapshot cleanup (`termixgo clean-snapshots`)
  with `--keep` and `--dry-run` support, plus automatic 20-snapshot pruning during
  snapshot creation to prevent disk volume exhaustion.
- `doctor --fix` flag: automated permission hardening for `config.json`,
  `secrets.json`, and state directories to private owner-only access (0600/0700).
- MCP per-tool disable: `disabledTools` configuration on MCP servers, filtering
  disabled tools from the model catalog and rejecting calls before process spawn.
- Socket-level metadata guard: dialer control (`blockMetadataAddress`) blocking
  connections to cloud metadata (`169.254.169.254`, `fd00:ec2::254`) and link-local
  addresses even when reached via DNS rebinding.
- Partitioned token ledger: usage ledger and `/cost` command distinguish between
  pay-per-token BYOK API keys and subscription-based OAuth quotas.

## 0.2.0 - 2026-10-25

### Added

- Configurable tool hooks. A `pre_tool` hook can block a call (exit status 2)
  and a `post_tool` hook can hand feedback to the model, for example a linter's
  verdict on the edit that just ran. Hooks come from `~/.termixgo/config.json`
  only, receive the call as JSON on stdin, and fail open with a notice when they
  break or time out.
- Evaluation benchmark harness (`termixgo eval`). Run benchmark suites in
  isolated workspaces to measure coding, bugfixing and refactoring accuracy,
  reporting pass rate, duration, steps, tokens and cost in tabular or JSON form.
- Headless NDJSON event streaming (`termixgo run --json`). Stream line-delimited
  JSON events directly to stdout for CI pipelines, scripts and automation tools.
- Immediate post-edit syntax diagnostics. File mutation tools (`edit`, `multi_edit`,
  `write_file`, `apply_patch`) run fast in-memory syntax validation on changed files
  (Go, JSON, YAML, Python, JavaScript) and append any syntax errors directly to the
  tool result output for immediate autonomous correction. Can be checked on demand
  with `check_diagnostics` or disabled via `postEditDiagnosticsDisabled` in config.
- Static code and security audit scanner (`audit_code` tool and `termixgo audit code`).
  Inspects workspace files for security vulnerabilities (injection, insecure TLS,
  weak crypto), hardcoded secrets (API keys, private keys, database URIs),
  concurrency leaks, and bad error handling, with tabular or JSON reporting.
- Repository map generator (`repo_map` tool, `termixgo map`, and `/map` slash command).
  Extracts structural symbol maps (exported types, interfaces, methods, functions)
  within a token budget for an instant architectural mental model of the codebase.
- Atomic cross-file refactoring engine (`refactor_rename` tool). Safely renames
  functions, variables, types or classes across multiple files in the workspace
  with preview, word-boundary matching, automatic file backups and post-rename syntax diagnostics.
- Interactive TUI slash commands for audit, diagnostics, benchmark evaluations, repository maps, and diff review
  (`/audit code`, `/diagnostics`, `/eval`, `/map`, `/testgen`, `/diff-review`).
- Autonomous test generation and verification synthesizer (`generate_tests` tool,
  `termixgo test-gen`, and `/testgen` slash command). Parses AST function signatures
  and type definitions to synthesize table-driven unit tests, avoids duplicate test
  creation against existing test suites, and executes automated verification runs.
- Pre-commit diff inspection and code review engine (`review_changes` tool, `ReviewDiff`,
  `termixgo review`, and `/diff-review` slash command). Analyzes unified diff additions
  for merge conflict markers, hardcoded secrets, leftover debug statements (fmt.Print,
  console.log, debugger, pdb) and syntax errors, and combines static findings with
  read-only LLM subagent review in terminal text or JSON format.
- Standalone Windows installer executable (`TermixgoSetup.exe`) and automated builder
  script (`scripts/build-windows-installer.ps1`). Self-contained single-file installer
  that installs per-user without admin rights, registers in Windows settings, updates user
  PATH with instant broadcast, creates Start Menu shortcuts, and supports silent installs.

### Fixed

- The "no API key" journal hint never fired because a lowercased error was
  compared with a mixed-case literal.
- A multi-line memory fact lost its tail on read, and a line starting with `- `
  inside it became a second fact. A fact is now stored as one bullet.

## 0.1.6 - 2026-10-09

### Added

- `symbol_search` finds where a function, method, type, class or other named
  symbol is defined across the workspace. Go files are parsed precisely, and a
  method matches through its receiver type; Python, TypeScript, JavaScript,
  Rust and Java are matched structurally.
- `undo_edit` restores a file to what it held before the last writes. Every
  `write_file`, `edit`, `multi_edit` and `apply_patch` keeps a bounded per-file
  backup, so one call reverts one write and `steps` reverts several. The
  approval dialog previews the revert like any other write.

### Changed

- `read_file` streams a file larger than 2 MB line by line, so an offset and
  limit window over a huge generated file no longer loads the whole thing into
  memory.
- `apply_patch` plans every file before writing any, so a patch whose later
  operation cannot apply leaves the tree unchanged. It chains two updates of
  one file, rejects an unknown `***` directive instead of misattaching its
  hunks, keeps star-prefixed content and trailing spaces, and preserves CRLF
  endings.
- A dev server, file watcher or any long-running command belongs in
  `run_background`: the system prompt and `run_command` say so, a timeout whose
  output reads as a starting server suggests the retry, and the real background
  tool names (`run_background`, `run_logs`, `run_wait`, `run_list`, `run_kill`)
  are advertised under tool search and the terminal-first harness.
- The stored session transcript is bounded to the same context budget the
  request uses, so the session file and memory stop growing for the life of a
  session; a one-time notice tells the operator when it first trims.
- A subagent inherits the operator's allow-always and allow-session answers
  instead of re-asking inside every delegated task.
- An attached image whose longest side exceeds 8192 pixels is scaled down
  before it is sent, so a full-page screenshot is no longer rejected with a
  generic unsupported-image error and no longer poisons the session.
- An extra tool whose alias collides with a builtin is skipped whole rather
  than silently taking over the builtin's calls.
- `Redact` shows one fixed mask for a short secret, so its length stays hidden.
- The workspace search and the file index skip the `.termixgo` and `.termigo`
  state trees.
- A failing git command is named with its full arguments.

### Fixed

- The auto file checkpoint bounds its walk: it stops at a file and byte cap,
  honours the checkpoint timeout, and skips generated and state trees, so a
  turn in a huge workspace (a filesystem root) no longer stalls before the
  model is called.
- The git stash checkpoint measures the untracked, non-ignored volume before it
  copies it, and leaves the whole untracked side out when it is a dependency or
  build tree, so an unignored `node_modules` cannot put hundreds of megabytes
  into the repository's object store for good.
- A checkpoint taken while the only changes sat under `.termixgo/` no longer
  replays an older checkpoint over the working tree. The stash pathspec
  excludes that directory, so the push saved nothing, and the newest earlier
  entry was then applied as if it were the new one. Status now reads through
  the same pathspec and the apply only runs when `refs/stash` actually moved.
- `git_blame` validates its revision with the same rule as `git_diff`.
- The Antigravity OAuth provider now sends the required Cloud Code thinking
  tier suffix for Claude models (`claude-opus-5-5-high` and `claude-sonnet-5-5-medium`),
  avoiding 404 model_not_found errors from Google's backend.

## 0.1.5 - 2026-10-03

### Changed

- The README now spells TERMIXGO like the TUI does: a coloured SVG banner
  (`termixgo-banner.svg`) built from the same block glyphs and the same
  teal-to-amber per-letter hues as `internal/ui/banner.go`, replacing the
  thin figlet art. The Install section points at the `installer/` folder as
  the run-from-source entry point, and the screenshot now shows the welcome
  screen on launch (`termigo-welcome.png`) instead of the old VPS transcript
  (`termixgo-vps.png`).
- The Windows installer scripts live in `installer/` now (they moved from
  `packaging/windows/`): the double-click `install.bat`, the per-user
  `install.ps1`, `uninstall.ps1` and a folder README, with
  `scripts/make-installer.ps1` staging the release bundle from there. The
  folder also doubles as a run-from-source install point: build
  `installer/termixgo.exe` and run `install.bat` beside it.
- The wordmark is bold now: the welcome banner spells TERMIXGO in full-block
  glyphs, one hue per letter on a teal-to-amber ramp, instead of thin
  underscore strokes. The rows stay rectangular so the welcome block clips
  them safely on narrow terminals.
- `/setup` and `/model` carry the plain wordmark and the running version
  (`Termixgo 0.1.4 Setup: Provider`, `Termixgo 0.1.4: Choose a provider`),
  so any screenshot of a menu names the build it came from.

### Fixed

- In an untrusted folder, `allow session` and `always` at the trust prompt
  actually stick now. The folder gate re-asked every mutating tool on every
  turn whatever the operator answered, because the answer was never recorded
  anywhere the gate reads. Session answers now live for the run and
  folder-always answers persist under the folder (a new `folderAllowedTools`
  config key), re-seeded into every turn's environment. The security design
  stands: a folder-level grant only silences this folder's gate, and
  tool-level `always-allowed` entries - statements about a tool, not the
  folder - still do not unlock writes somewhere new.

### Added

- The transcript now shows what an edit changed: the diff preview the approval
  dialog already computed before the run rides on the tool-start event, and a
  finished, successful edit draws it inside a bordered box as a code review - a
  removed line red, an added line blue and an unchanged line green, the way the
  Muse CLI shows an edit. Compact view peeks a few rows, the details toggle
  shows more, and a long diff ends in a "... (N more lines)" marker. A running
  or failed tool shows nothing - a preview of a change that did not happen
  would lie.
- `review_changes` runs the read-only reviewer over the working change set
  before a commit, and a reviewer that opens no file no longer gets to say
  "Looks good.": `RunSubagent` counts the nested run's tool calls, retries an
  empty read-tier pass once with an explicit mandate, and returns an error
  when the second pass also inspected nothing. Worker roles are exempt.
- `git_diff` takes a `base` revision (a sha, branch or range) behind
  `--end-of-options`, so a change set can be compared against a commit
  instead of only the working tree.
- `git_push` and `git_pull`: a plain push to the current upstream, and a
  fast-forward-only pull. Neither takes model-chosen flags; a force push has
  to go through `run_command`, where the operator reads it in the approval.
- The tool registry folds camelCase, hyphen and space spellings onto the
  registry name before reporting a miss, so `ReadFile` or `git-diff` reaches
  the real tool instead of costing a round on the did-you-mean path.
- The subagent roster gains `vision` and the in-scope security family
  (`pentest`, read-tier `pentest-recon`, `pentest-web`, `pentest-network`).
  The read-tier registry now carries the non-mutating network tools
  `web_fetch`, `web_search` and `probe_url`, so recon can map a surface
  without ever reaching a mutating tool.

## 0.1.4 - 2026-10-03

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
