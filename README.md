# Termixgo

Termixgo is a coding agent that runs entirely in your terminal. No web view, no
browser, no separate runtime. One compiled binary, your own API keys, and
everything the agent does is printed as terminal output.

```
 _____  _____  ____   __  __  ___ __  __  ____   ___
|_   _|| ____||  _ \ |  \/  ||_ _|\ \/ / / ___| / _ \
  | |  |  _|  | |_) || |\/| | | |  \  / | |  _ | | | |
  | |  | |___ |  _ < | |  | | | |  /  \ | |_| || |_| |
  |_|  |_____||_| \_\|_|  |_||___|/_/\_\ \____| \___/
```

- Runs on Windows, Linux and macOS from a single binary.
- Written in Go with Bubble Tea. Measured release build: 8.6 MB binary and
  about 10 MB working set at idle, so a 2 GB machine is comfortable and the
  process never approaches 500 MB.
- Bring your own key (BYOK). Keys are stored locally and never leave your
  machine except to call the provider you chose.
- Streams what the agent is doing: thinking, reasoning, each tool call, and
  the command output behind it.
- Optional Telegram companion so you can keep a run going from your phone.

## Build

Requires Go 1.26 or newer.

```sh
go build -o bin/termixgo ./cmd/termixgo
```

Cross-compile for every platform:

```sh
make cross
```

## First run

```sh
./bin/termixgo
```

On first use Termixgo opens the onboarding wizard:

1. Pick a provider.
2. Paste an API key. It is stored in `~/.termixgo/secrets.json`, readable only
   by your account, and is never displayed again.
3. Pick the default model for that provider.
4. Optionally connect a Telegram bot: create one with `@BotFather`, paste the
   token, then send `/pair <code>` from your chat.

The wizard is also available any time with `/setup`.

If you prefer to configure things from the shell:

```sh
./bin/termixgo secret anthropic          # prompts, input hidden
./bin/termixgo model claude-sonnet-4-5
./bin/termixgo trust on                  # trust this folder
./bin/termixgo doctor                    # show what is configured
```

## Using it

Type a request and press Enter:

```
> add a /health endpoint and a test for it
> + Reading src/server/routes.go
> + Grepping "/health"
> + Editing src/server/routes.go
> + Wrote src/server/routes_test.go
> + Running pnpm run test
  + Ran pnpm run test (4231ms)
Added GET /health returning 200 with an uptime field, and a test that asserts
the status code and body shape. The suite passes: 41 tests, 0 failures.
```

What you see while it works:

| Line | Meaning |
| --- | --- |
| `Thinking...` | the model's reasoning is streaming in |
| `Reasoned for 4s` | the reasoning block finished, with its duration |
| `> Reading foo.go` | a tool call is running |
| `+ Read foo.go (12ms)` | the tool finished |
| `x Running ...` | the tool failed, with the output in the transcript |
| `[x] Fix the parser` | the plan, updated as work progresses |

Enter sends. `Ctrl+J` inserts a newline. `Tab` completes a slash command.
`Esc` stops a running turn, then clears the input. `Ctrl+C` quits.

## Slash commands

| Command | What it does |
| --- | --- |
| `/model [id]` | show or switch the model (no argument opens a picker) |
| `/setup` | onboarding: provider key, model, skills, Telegram |
| `/help` | every command and key binding |
| `/new` | start a new session |
| `/sessions` | list and resume recent sessions |
| `/stop` | stop the running turn |
| `/status` | workspace, model, plan and token status |
| `/trust [on\|off]` | show or change folder trust |
| `/approval [ask\|edits\|all]` | when a tool waits for you |
| `/plan` | show the current task plan |
| `/tools` | list the tools the agent can call |
| `/mcp [reload]` | show the MCP servers and the tools they add |
| `/skills [reload]` | list loaded skills |
| `/memory` | show what the agent has learned |
| `/telegram [setup\|on\|off\|status\|pair]` | manage the companion bot |
| `/init` | generate a `TERMIXGO.md` for the project |
| `/cost` | token usage and estimated spend for this session |
| `/ps [kill <handle>]` | list background processes, or stop one |
| `/exit` | quit |

## Tools the agent can call

Filesystem: `read_file`, `write_file`, `edit`, `multi_edit`,
`create_directory`, `delete_file`, `move_file`, `list_directory`.
Search: `grep`, `glob`.
Execution: `run_command`, `run_checks` (detects the project's own test, lint,
typecheck and build commands for Go, Rust, Node, Python and Make projects).
Background: `run_background`, `run_logs`, `run_wait`, `run_list`, `run_kill`.
Git: `git_status`, `git_diff`, `git_log`, `git_show`, `git_add`, `git_commit`,
`git_branch`, `git_restore`.
Planning: `todo_write`, `todo_read`.
Knowledge: `remember`, `use_skill`, `find_skill`.
Interaction: `ask_user`, `think`, `run_subagent`, `web_fetch`.
MCP: every tool your configured servers publish, as
`mcp_<server>__<tool>`.

`run_command` is how the agent runs `biome`, `vitest`, `cargo clippy`,
`pytest`, `golangci-lint` and anything else your project uses; the output is
streamed back into the transcript.
## Providers

OpenAI, Anthropic, Google, xAI, Cerebras, Groq, DeepSeek, Qwen, Zhipu,
Mistral, OpenRouter, any OpenAI-compatible endpoint, LM Studio, MLX and Ollama.

A key can also come from the environment, which is convenient in CI:
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `GROQ_API_KEY`,
`DEEPSEEK_API_KEY`, `MISTRAL_API_KEY`, `OPENROUTER_API_KEY`, `XAI_API_KEY`,
`CEREBRAS_API_KEY`, `DASHSCOPE_API_KEY`, `ZHIPU_API_KEY`.

Local servers need no key. `openai-compatible` and the local providers take a
base URL from `baseUrls` in the config file or from `/setup`.

## Trust and approval

Termixgo treats folders as trusted or untrusted. A fresh folder is untrusted:
writes and commands ask first. Approve once, for the session, or always. Once a
folder is trusted, the agent works without interrupting you, which is the mode
the desktop build uses everywhere.

`/approval` sets the policy independently: `ask` (everything waits), `edits`
(file edits run, commands wait), `all` (nothing waits).

## Memory and skills

- Project memory: the first of `TERMIXGO.md`, `AGENTS.md` or `CLAUDE.md` at the
  workspace root.
- Learned memory: `.termixgo/memory.md` per project, `~/.termixgo/memory.md`
  globally. The agent appends with the `remember` tool.
- Skills: `.termixgo/skills/<name>/SKILL.md` in the project, or
  `~/.termixgo/skills/<name>/SKILL.md` globally. Only the short description
  enters the prompt; the body loads on demand.

## MCP servers

Termixgo is an MCP client over stdio. A server you configure contributes its
tools to the agent, so a tool you already run becomes a tool the agent can call.

```json
{
  "mcpServers": [
    {
      "name": "files",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]
    }
  ]
}
```

Add that to `~/.termixgo/config.json` and run `/mcp reload`. The server starts
with the session and its tools appear as `mcp_<server>__<tool>`, so a name like
`mcp_files__read_file` always says where the call went.

- `/mcp` lists every configured server, whether it started, and how many tools
  it added.
- `termixgo mcp` does the same from a shell, and exits non-zero when a server
  fails to start, which makes it usable in a check.
- `disabled` parks a server without deleting its command line.

Two things are worth knowing before you trust a server:

- Every contributed tool is approval gated, and in an untrusted folder it asks
  before it runs. A server can lower that by publishing a `readOnlyHint`, which
  is treated as a claim rather than a guarantee: a tool that says nothing is
  treated as one that can change things.
- The protocol is on the server's stdio, so a server that prints a banner on
  stdout will not work. Its stderr is kept and shown in the failure message,
  which is usually what names the missing package.

Only stdio transport is implemented, and only `initialize`, `tools/list` and
`tools/call`. Resources, prompts and sampling are out of scope: the agent
already has its own answer for each.

## Telegram companion

```
/telegram setup     # paste a token, get a pairing code
/telegram status
/telegram off
```

The bot accepts `/run <prompt>`, `/stop`, `/new`, `/model`, `/status`, `/help`
and plain text. It refuses every chat until paired, and pins the owner user id
once paired. Progress is mirrored into one edited message instead of a flood.

## Cost

The status bar and `/cost` show estimated spend for the session, from published
list prices:

```
$ /cost
Tokens: 41230 in, 8114 out, 49344 total.
Estimated spend: about $0.2457 (budget $2.00).
Prices are published list values, an estimate not a bill. Set a cap with costBudgetUsd in config.
```

To cap a session, add a budget to `~/.termixgo/config.json`:

```json
{ "costBudgetUsd": 2.00 }
```

The run stops before the step that would pass the budget and says so:
`stopped: estimated spend reached $2.1043 of the $2.00 budget`. It never stops
silently.

Three honest caveats:

- Prices are list values in a table in `internal/provider/pricing.go`. Vendors
  change them. Treat the figure as a budget guide, not an invoice.
- Local models (Ollama, LM Studio, MLX) report as free rather than unknown, so
  a zero next to a local model means what it says.
- A model with no recorded price reports `cost unknown for this model` and no
  budget will fire, because there is nothing to compare against. When a cap is
  set on such a model, both `/cost` and `termixgo doctor` say so explicitly
  rather than leaving you to discover it from a bill:

```
$ termixgo doctor
cost budget:    $2.00 per session
                WARNING: no price is known for this model, so the cap cannot fire.
                Add one with modelPricing, for example {"my-model":{"inputPerMillion":3,"outputPerMillion":9}}.
```

To give an unpriced model a price, add it to the same file:

```json
{ "modelPricing": { "brand-new-model": { "inputPerMillion": 3.00, "outputPerMillion": 9.00 } } }
```

The cap then applies to that model. `modelPricing` is matched by id, so an
entry keeps working when a vendor renames a model behind a stable local id.

## Signals and cancellation

`Ctrl+C` cancels the turn in flight rather than killing the process: the run
loop stops between steps and the session is saved. In the full-screen UI,
`Esc` stops the turn without leaving. `termixgo run` in a script responds to
`SIGINT` and `SIGTERM` the same way.

A one-shot `termixgo run` never writes a session file. A hundred invocations
leave a hundred nothing, which is what makes it safe in CI.

## Security

Read [`SECURITY.md`](SECURITY.md) before running Termixgo somewhere that
matters. The short version:

- **The secret file grants access to you alone**, on every platform. On Linux
  and macOS that is mode 0600; on Windows it is an explicit ACL that replaces
  the inherited one, because `os.Chmod` only toggles the read-only attribute
  there and a shared local group could otherwise read your API keys.
  `termixgo doctor` reports the real state rather than assuming it.
- **There is no sandbox.** Tools run with your permissions. Folder trust and
  the approval policy are the only gates.
- **A cost cap is an estimate, not a guarantee.** It compares tokens against
  list prices, and it cannot fire for a model with no recorded price. Both
  `/cost` and `doctor` say so when that is the case.
- **Keys are stored as plaintext** in the state directory, not in the OS
  keychain. `doctor` tells you who can read the file.

Report vulnerabilities privately through GitHub security advisories, not a
public issue.

## Where state lives

| Path | Contents |
| --- | --- |
| `~/.termixgo/config.json` | model, approval mode, trust list, Telegram pairing |
| `~/.termixgo/secrets.json` | provider keys and the bot token, owner-only |
| `~/.termixgo/sessions/*.json` | saved conversations, owner-only |
| `~/.termixgo/memory.md` | global learned memory |
| `<workspace>/.termixgo/` | project memory and skills |

Set `TERMIXGO_HOME` to move the state directory, which is useful for tests and
throwaway profiles.

## CLI reference

```sh
termixgo                        # terminal UI
termixgo setup                  # open the onboarding wizard
termixgo run "<prompt>"         # one prompt, streamed to stdout
termixgo models [--provider id]
termixgo model [id]
termixgo trust [on|off]
termixgo approval [mode]
termixgo secret <provider> [key]
termixgo telegram [status|on|off]
termixgo doctor
termixgo version
```

`termixgo run` and any invocation without a TTY fall back to a plain streaming
mode, so it composes with pipes and CI. It exits non-zero when the turn failed,
so a pipeline can tell a rejected key from a completed answer.

## Development

All work happens in the terminal:

```sh
make check          # gofmt, go vet, go build, go test, then race
make test
make race           # unit tests under the race detector
make lint           # go vet, plus staticcheck when installed
make build
```

`make check` runs the race detector where a C compiler exists. On Windows that
is `scoop install mingw`, which needs no administrator rights; on Debian it is
`build-essential`. `scripts/race.ps1` and `scripts/race.sh` find a compiler,
enable cgo, and say what to install when there is none, so the check reports
"skipped" instead of failing on a machine that cannot run it. CI runs the
detector on Linux for every change.

On Windows without `make`:

```powershell
powershell -File scripts/check.ps1
```

The layout, invariants and conventions are in `TERMIXGO.md`; agent instructions
are in `AGENTS.md`.

## Reliability

The things that decide whether a long run finishes:

- **Per-model context budget.** History is trimmed against the window of the
  model you picked, not one global guess: a 1M-token Gemini gets room, an 8k
  local model is not silently overfilled. The budget never drops below 30
  percent of the window, and `status`/`/status` show how much is in use.
- **Retries with backoff.** A 429 or a 5xx pause and retry up to four times,
  honouring `Retry-After`. A 401 or 404 fails immediately, because retrying a
  wrong key only wastes time.
- **Stall watchdog.** If a provider accepts the request and then goes silent
  for 90 seconds, the stream is aborted with a clear message instead of
  hanging the terminal.
- **One turn at a time.** A second request is told the agent is busy rather
  than queued behind work it cannot see.
- **Responsive Telegram.** Updates are handled concurrently, so `/stop`,
  `/status` and `/model` are answered while a run is in progress, which is
  exactly when they matter.

## Dev tooling

| Tool | Role |
| --- | --- |
| `gofmt -s` | formatting |
| `go vet` | lint |
| `go test` | unit tests |
| `go test -race` | data races, when a C toolchain is present |
| `staticcheck` | extra lint, when installed |

The agent can also run *your* project's tooling. `run_checks` and
`run_command` drive `biome`, `vitest`, `cargo`, `pytest`, `golangci-lint` and
anything else, and their output is streamed into the transcript.

## Releasing

```sh
git tag v0.2.0
git push origin v0.2.0
```

The release workflow runs the full gate, then goreleaser builds six targets,
writes `checksums.txt` and opens a draft release. Verify a download against
that file before running it.

The version, commit and build date are stamped into the binary through
ldflags, which is only possible because `internal/version` declares them as
vars rather than consts:

```sh
go build -ldflags "-X github.com/99apps-id/termixgo/internal/version.Version=9.9.9" ./cmd/termixgo
```

## Development history

See [`CHANGELOG.md`](CHANGELOG.md) for what changed and
[`CONTRIBUTING.md`](CONTRIBUTING.md) for how to build, test and contribute.

## Design notes

- **Why Go.** A single static binary with no runtime, no `node_modules`, and
  predictable memory. The TUI is Bubble Tea; the provider clients are plain
  `net/http` and JSON.
- **Why a flat message model.** One `Message` shape covers OpenAI-style,
  Anthropic and Google wire formats, which keeps sessions, compaction and the
  run loop small and testable.
- **Streaming.** The provider client parses Server-Sent Events and emits
  deltas; the run loop turns them into events; the UI turns events into
  transcript blocks. Nothing blocks the terminal while a model thinks.
- **No sandbox theatre.** Tools run with the operator's own permissions. The
  approval policy, not a pretend sandbox, is what protects the machine, and it
  is explicit about which tool needs which level of trust.

## License

Apache-2.0. See [`LICENSE`](LICENSE) for the full text and [`NOTICE`](NOTICE) for
attribution. Third-party Go modules keep their own licences; the exact versions
in use are in `go.sum`.
