# Security Policy

## Reporting a vulnerability

Report suspected vulnerabilities privately through GitHub's
[security advisory](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
form on this repository. Please do not open a public issue for a security
problem.

Include what you can: the version (`termixgo version`), the platform, what you
did, what happened, and what you expected. A proof of concept helps.

Expect an acknowledgement within a few days. Please give a reasonable window
for a fix before disclosing publicly.

## Supported versions

Only the latest release receives security fixes until the project declares
otherwise. Development builds are not supported.

## What Termixgo protects

| Asset | Protection |
| --- | --- |
| Provider API keys | `secrets.json` in the state directory, owner-only access enforced per platform, never logged, never written to config or a session |
| Telegram bot token | Same store, same rules |
| Command execution | The agent runs with your own permissions. There is no sandbox. The approval policy and folder trust are the only gates |
| Model traffic | HTTPS to the provider you configured. No proxy, no telemetry, no analytics |

## Known limits you should decide about

These are deliberate tradeoffs, not oversights. Read them before running
Termixgo on a machine that matters.

1. **No sandbox.** Tools run as you, with your permissions. A prompt that
   convinces the model to run a destructive command will run it. Folder trust
   and the approval policy are the only controls, and both default to
   permissive once a folder is trusted.
2. **Secrets are stored as plaintext** JSON with owner-only permissions, not in
   the OS keychain. On Linux and macOS the file is mode 0600; on Windows it
   carries an explicit ACL that grants only the current user. Anyone who can
   read your user profile can read the file.
3. **No cost cap.** Nothing stops a runaway loop from spending money with your
   API key. Watch the token counts in the status bar and use `/stop`.
4. **The agent can reach the network.** `run_command` and `web_fetch` are not
   restricted by default. Prompt injection from fetched content is a real risk.
5. **The Telegram bot accepts commands from the paired chat.** Pairing pins the
   owner user id, and an unpaired bot refuses everything, but anyone who
   obtains your bot token can talk to it.

## Hardening suggestions

- Run Termixgo in a container or a throwaway VM when the model may run
  commands you have not reviewed.
- Leave the approval policy on `ask` for repositories you do not control.
- Keep separate API keys per tool so a leak is easy to revoke and its blast
  radius is small.
- Revoke a key immediately at the provider if you ever paste it into a shared
  terminal, a recording, or a public issue.
